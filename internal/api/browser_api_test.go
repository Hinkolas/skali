package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/podexec"
)

func (a *testAPI) browserRequest(method, path string, cookie *http.Cookie, body any) (*http.Response, map[string]any) {
	a.t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(a.t, err)
	r, err := http.NewRequest(method, a.srv.URL+path, bytes.NewReader(raw))
	require.NoError(a.t, err)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Requested-With", "skali")
	r.Header.Set("Origin", a.srv.URL)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	res, err := a.srv.Client().Do(r)
	require.NoError(a.t, err)
	defer res.Body.Close()
	var result map[string]any
	if res.StatusCode != 204 {
		require.NoError(a.t, json.NewDecoder(res.Body).Decode(&result))
	}
	return res, result
}

func TestBrowserSessionLifecycle(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("browser@example.com", "hunter2hunter2")
	res, body := a.browserRequest("POST", "/api/v1/auth/login", nil, map[string]string{"email": "browser@example.com", "password": "hunter2hunter2", "session_transport": "cookie"})
	require.Equal(t, 200, res.StatusCode, body)
	require.NotContains(t, body["session"], "token")
	require.Len(t, res.Cookies(), 1)
	cookie := res.Cookies()[0]
	require.True(t, cookie.HttpOnly)
	require.False(t, cookie.Secure, "HTTP test server")
	require.Empty(t, cookie.Domain)
	require.Equal(t, "no-store", res.Header.Get("Cache-Control"))
	res, body = a.browserRequest("GET", "/api/v1/auth/session", cookie, nil)
	require.Equal(t, 200, res.StatusCode, body)
	require.Equal(t, "browser@example.com", body["user"].(map[string]any)["email"])
	res, body = a.browserRequest("POST", "/api/v1/users", cookie, map[string]string{})
	require.Equal(t, 403, res.StatusCode, body)
	a.staleAllSessions()
	res, body = a.browserRequest("POST", "/api/v1/auth/2fa/enable", cookie, nil)
	require.Equal(t, 403, res.StatusCode, body)
	require.Equal(t, "reauth_required", errorCode(t, body))
	res, body = a.browserRequest("POST", "/api/v1/auth/reauth", cookie, map[string]string{"password": "hunter2hunter2"})
	require.Equal(t, 204, res.StatusCode, body)
	res, body = a.browserRequest("POST", "/api/v1/auth/logout", cookie, nil)
	require.Equal(t, 204, res.StatusCode, body)
	cookies := res.Cookies()
	require.NotEmpty(t, cookies)
	require.Equal(t, -1, cookies[len(cookies)-1].MaxAge)
	res, body = a.browserRequest("GET", "/api/v1/auth/session", cookie, nil)
	require.Equal(t, 401, res.StatusCode, body)
	require.Equal(t, -1, res.Cookies()[0].MaxAge)
	// The same endpoint still serves legacy CLI clients without setting cookies.
	status, body := a.do("POST", "/v1/auth/login", "", map[string]string{"email": "browser@example.com", "password": "hunter2hunter2"})
	require.Equal(t, 200, status)
	require.NotEmpty(t, body["session"].(map[string]any)["token"])
}

func TestBrowserTwoFactorLogin(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("browser@example.com", "hunter2hunter2")
	token := a.login("browser@example.com", "hunter2hunter2")
	status, body := a.do("POST", "/v1/auth/2fa/enable", token, nil)
	require.Equal(t, 200, status)
	code, err := totp.GenerateCode(body["secret"].(string), time.Now())
	require.NoError(t, err)
	backup := body["backup_codes"].([]any)[0].(string)
	status, body = a.do("POST", "/v1/auth/2fa/confirm", token, map[string]string{"code": code})
	require.Equal(t, 204, status, body)
	res, body := a.browserRequest("POST", "/v1/auth/login", nil, map[string]string{"email": "browser@example.com", "password": "hunter2hunter2", "session_transport": "cookie"})
	require.Equal(t, 200, res.StatusCode)
	require.Empty(t, res.Cookies())
	require.NotContains(t, body, "session")
	challenge := body["challenge"].(map[string]any)["token"].(string)
	res, body = a.browserRequest("POST", "/v1/auth/2fa/verify", nil, map[string]string{"challenge_token": challenge, "code": backup, "session_transport": "cookie"})
	require.Equal(t, 200, res.StatusCode, body)
	require.NotContains(t, body["session"], "token")
	require.Len(t, res.Cookies(), 1)
	res, body = a.browserRequest("POST", "/v1/auth/2fa/verify", nil, map[string]string{"challenge_token": challenge, "code": backup, "session_transport": "cookie"})
	require.Equal(t, 401, res.StatusCode, body)
	require.Empty(t, res.Cookies())
}

func TestBrowserLogoutPreservesCookieOnBackendFailure(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("browser@example.com", "hunter2hunter2")
	res, body := a.browserRequest("POST", "/v1/auth/login", nil, map[string]string{
		"email": "browser@example.com", "password": "hunter2hunter2", "session_transport": "cookie",
	})
	require.Equal(t, 200, res.StatusCode, body)
	cookie := res.Cookies()[0]
	// Authentication still works; only the subsequent revocation fails.
	_, err := a.st.Pool.Exec(t.Context(), `
		CREATE FUNCTION reject_session_delete() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'temporary revocation failure'; END $$;
		CREATE TRIGGER reject_session_delete BEFORE DELETE ON sessions
		FOR EACH ROW EXECUTE FUNCTION reject_session_delete();`)
	require.NoError(t, err)
	res, body = a.browserRequest("POST", "/v1/auth/logout", cookie, nil)
	require.Equal(t, 500, res.StatusCode, body)
	for _, responseCookie := range res.Cookies() {
		require.NotEqual(t, -1, responseCookie.MaxAge)
	}
	res, body = a.browserRequest("GET", "/v1/auth/session", cookie, nil)
	require.Equal(t, 200, res.StatusCode, body)
}

func TestBrowserExecHandshake(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("browser@example.com", "hunter2hunter2")
	token := a.login("browser@example.com", "hunter2hunter2")
	envID := a.execEnvironment(t, token)
	var resolutions atomic.Int32
	a.execFake.script(func(context.Context, uuid.UUID, podexec.Options) (*podexec.Session, error) {
		resolutions.Add(1)
		return &podexec.Session{}, nil
	}, func(context.Context, *podexec.Session, podexec.Streams) error { return nil })
	address := "ws" + strings.TrimPrefix(a.srv.URL, "http") + "/api/v1/environments/" + envID.String() + "/exec?service=web"
	headers := http.Header{"Cookie": {(&http.Cookie{Name: SessionCookie, Value: token}).String()}}
	for _, origin := range []string{"", "https://evil.test"} {
		headers.Set("Origin", origin)
		conn, response, err := websocket.DefaultDialer.Dial(address, headers)
		require.Error(t, err)
		require.Nil(t, conn)
		require.Equal(t, 403, response.StatusCode)
		response.Body.Close()
		require.Zero(t, resolutions.Load(), "reject before resolving an exec session")
	}
	headers.Set("Origin", a.srv.URL)
	conn, response, err := websocket.DefaultDialer.Dial(address, headers)
	require.NoError(t, err)
	defer conn.Close()
	require.Equal(t, 101, response.StatusCode)
	require.EqualValues(t, 1, resolutions.Load())
	require.NotEmpty(t, response.Cookies(), "sliding expiry survives the upgrade response")
	readTranscript(t, conn)
}
