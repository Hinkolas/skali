package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/Hinkolas/skali/internal/auth"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/stretchr/testify/require"
)

type browserAuthenticator func(context.Context, string) (*store.User, *store.Session, error)

func (f browserAuthenticator) Authenticate(ctx context.Context, token string) (*store.User, *store.Session, error) {
	return f(ctx, token)
}

func TestCookieAuthenticationPolicy(t *testing.T) {
	expires := time.Now().Add(24 * time.Hour).Truncate(time.Second).UTC()
	for _, tc := range []struct {
		name, method, authorization, origin, header string
		supplied                                    bool
		authErr                                     error
		status                                      int
		cleared                                     bool
	}{
		{name: "cookie read", method: "GET", status: 204},
		{name: "cookie mutation", method: "POST", origin: "https://skali.test", header: "skali", status: 204},
		{name: "missing csrf", method: "POST", status: 403},
		{name: "cross origin", method: "POST", origin: "https://app.skali.test", header: "skali", status: 403},
		{name: "invalid bearer overrides cookie", method: "GET", authorization: "broken", supplied: true, status: 401},
		{name: "rejected bearer overrides cookie", method: "GET", authorization: "Bearer cli", supplied: true, authErr: auth.ErrInvalidToken, status: 401},
		{name: "empty bearer overrides cookie", method: "GET", supplied: true, status: 401},
		{name: "bearer mutation", method: "POST", authorization: "Bearer cli", supplied: true, status: 204},
		{name: "expired cookie", method: "GET", authErr: auth.ErrInvalidToken, status: 401, cleared: true},
		{name: "backend unavailable", method: "GET", authErr: errors.New("unavailable"), status: 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			authenticate := browserAuthenticator(func(_ context.Context, token string) (*store.User, *store.Session, error) {
				called = true
				if tc.supplied {
					require.Equal(t, "cli", token)
				} else {
					require.Equal(t, "browser", token)
				}
				return &store.User{}, &store.Session{ExpiresAt: expires}, tc.authErr
			})
			h := browserRequests(true, nil)(RequireAuth(authenticate)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, !tc.supplied, cookieAuthFrom(r))
				w.WriteHeader(204)
			})))
			r := httptest.NewRequest(tc.method, "https://skali.test/v1/auth/session", nil)
			r.AddCookie(&http.Cookie{Name: SessionCookie, Value: "browser"})
			if tc.supplied {
				r.Header.Set("Authorization", tc.authorization)
			}
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.header != "" {
				r.Header.Set("X-Requested-With", tc.header)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			require.Equal(t, tc.status, w.Code, w.Body.String())
			require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
			cookies := w.Result().Cookies()
			if tc.cleared {
				require.Len(t, cookies, 1)
				require.Equal(t, -1, cookies[0].MaxAge)
			} else if tc.status == 204 && !tc.supplied {
				require.Len(t, cookies, 1)
				c := cookies[0]
				require.True(t, c.HttpOnly)
				require.True(t, c.Secure)
				require.Empty(t, c.Domain)
				require.Equal(t, "/", c.Path)
				require.Equal(t, http.SameSiteLaxMode, c.SameSite)
				require.Equal(t, expires, c.Expires)
			} else {
				require.Empty(t, cookies)
			}
			if tc.status == 403 || (tc.supplied && tc.authorization != "Bearer cli") {
				require.False(t, called)
			}
		})
	}
}

func TestSessionTransportEnum(t *testing.T) {
	for _, input := range []string{`""`, `null`, `false`, `1`, `"unknown"`} {
		var transport sessionTransport
		require.Error(t, json.Unmarshal([]byte(input), &transport), input)
	}
	for _, input := range []string{"bearer", "cookie"} {
		var transport sessionTransport
		require.NoError(t, json.Unmarshal([]byte(`"`+input+`"`), &transport))
		require.Equal(t, sessionTransport(input), transport)
	}
}

func TestCookieWebSocketOrigin(t *testing.T) {
	for _, tc := range []struct {
		name, peer, proto, origin string
		want                      int
	}{
		{"direct same origin", "192.0.2.1:80", "", "http://skali.test", 404},
		{"missing origin", "192.0.2.1:80", "", "", 403},
		{"null origin", "192.0.2.1:80", "", "null", 403},
		{"sibling origin", "192.0.2.1:80", "", "http://app.skali.test", 403},
		{"trusted TLS proxy", "10.0.0.1:80", "https", "https://skali.test", 404},
		{"forged TLS header", "192.0.2.1:80", "https", "https://skali.test", 403},
		{"wrong scheme", "10.0.0.1:80", "https", "http://skali.test", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trust := func(ip netip.Addr) bool { return ip.String() == "10.0.0.1" }
			// A same-origin request reaches path validation (404); every other
			// request must fail before touching the intentionally nil exec service.
			h := browserRequests(true, trust)(realIP(trust)(http.HandlerFunc(newExecHandlers(nil, nil).open)))
			r := httptest.NewRequest("GET", "http://skali.test/v1/environments/invalid/exec", nil)
			r.RemoteAddr = tc.peer
			r.Header.Set("X-Forwarded-Proto", tc.proto)
			r.Header.Set("Origin", tc.origin)
			r = r.WithContext(context.WithValue(r.Context(), ctxKeyCookieAuth, true))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			require.Equal(t, tc.want, w.Code, w.Body.String())
		})
	}
}

func TestCookieLoginValidationBeforeAuthentication(t *testing.T) {
	for _, tc := range []struct {
		transport, header, origin string
		status                    int
	}{
		{"other", "skali", "https://skali.test", 400},
		{"cookie", "", "https://skali.test", 403},
		{"cookie", "skali", "https://evil.test", 403},
	} {
		t.Run(tc.transport+tc.header+tc.origin, func(t *testing.T) {
			r := httptest.NewRequest("POST", "https://skali.test/v1/auth/login", strings.NewReader(`{"email":"a@b.test","password":"password","session_transport":"`+tc.transport+`"}`))
			r.Header.Set("X-Requested-With", tc.header)
			r.Header.Set("Origin", tc.origin)
			w := httptest.NewRecorder()
			(&authHandlers{}).login(w, r)
			require.Equal(t, tc.status, w.Code)
		})
	}
}
