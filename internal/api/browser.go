package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const SessionCookie = "skali_session"

// The zero value means an omitted field (the legacy bearer contract).
// Explicit values must match the OpenAPI enum, including rejecting null.
type sessionTransport string

func (transport *sessionTransport) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	if value != "bearer" && value != "cookie" {
		return errors.New("session_transport must be bearer or cookie")
	}
	*transport = sessionTransport(value)
	return nil
}

type browserPolicy struct {
	secure bool
	scheme string
}

var crossOriginProtection = http.NewCrossOriginProtection()

// Capture proxy authority before realIP replaces the socket address.
func browserRequests(secure bool, trust func(netip.Addr) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			} else if peer, err := netip.ParseAddr(clientIP(r)); err == nil && trust != nil && trust(peer.Unmap()) {
				if values := r.Header.Values("X-Forwarded-Proto"); len(values) == 1 && (values[0] == "http" || values[0] == "https") {
					scheme = values[0]
				}
			}
			if strings.HasPrefix(r.URL.Path, "/v1/auth/") {
				w.Header().Set("Cache-Control", "no-store")
			}
			ctx := context.WithValue(r.Context(), ctxKeyBrowserPolicy, browserPolicy{secure: secure, scheme: scheme})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func policyFrom(r *http.Request) browserPolicy {
	if policy, ok := r.Context().Value(ctxKeyBrowserPolicy).(browserPolicy); ok {
		return policy
	}
	return browserPolicy{secure: true, scheme: "https"}
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: token, Path: "/", HttpOnly: true,
		Secure: policyFrom(r).secure, SameSite: http.SameSiteLaxMode, Expires: expires,
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Path: "/", HttpOnly: true, Secure: policyFrom(r).secure,
		SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

func cookieAuthFrom(r *http.Request) bool {
	value, _ := r.Context().Value(ctxKeyCookieAuth).(bool)
	return value
}

func safeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

func checkBrowserMutation(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("X-Requested-With") != "skali" || crossOriginProtection.Check(r) != nil {
		writeError(w, http.StatusForbidden, codeForbidden, "browser request requires a same-origin X-Requested-With: skali header")
		return false
	}
	return true
}

// Origin is mandatory for cookie-authenticated WebSockets, which use GET
// and therefore cannot rely on the normal mutation CSRF guard.
func sameOrigin(r *http.Request) bool {
	values := r.Header.Values("Origin")
	if len(values) != 1 {
		return false
	}
	origin, err := url.Parse(values[0])
	return err == nil && origin.User == nil && origin.Path == "" && origin.RawQuery == "" && origin.Fragment == "" &&
		origin.Scheme == policyFrom(r).scheme && strings.EqualFold(origin.Host, r.Host)
}

func validateSessionTransport(w http.ResponseWriter, r *http.Request, transport sessionTransport) bool {
	switch transport {
	case "", "bearer":
		return true
	case "cookie":
		return checkBrowserMutation(w, r)
	default:
		writeError(w, http.StatusBadRequest, codeBadRequest, "session_transport must be bearer or cookie")
		return false
	}
}
