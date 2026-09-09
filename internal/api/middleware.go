package api

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/Hinkolas/skali/internal/auth"
)

// RequireAuth guards a route group with bearer or cookie authentication and puts
// the resolved user, session, and raw token on the request context. It must
// wrap only the protected group — never the router root, or login locks
// itself out.
func RequireAuth(a auth.Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			token, ok := bearerToken(r)
			fromCookie := false
			if _, supplied := r.Header["Authorization"]; !supplied {
				if cookie, err := r.Cookie(SessionCookie); err == nil {
					token, ok, fromCookie = cookie.Value, cookie.Value != "", true
				}
			}
			if !ok {
				if fromCookie {
					clearSessionCookie(w, r)
				}
				writeError(w, http.StatusUnauthorized, codeInvalidToken, "missing or malformed session credentials")
				return
			}
			if fromCookie && !safeMethod(r.Method) && !checkBrowserMutation(w, r) {
				return
			}
			user, sess, err := a.Authenticate(r.Context(), token)
			if err != nil {
				if fromCookie && errors.Is(err, auth.ErrInvalidToken) {
					clearSessionCookie(w, r)
				}
				writeAuthError(r.Context(), w, err)
				return
			}
			if fromCookie {
				setSessionCookie(w, r, token, sess.ExpiresAt)
			}
			ctx := context.WithValue(r.Context(), ctxKeyCookieAuth, fromCookie)
			ctx = context.WithValue(ctx, ctxKeyUser, user)
			ctx = context.WithValue(ctx, ctxKeySession, sess)
			ctx = context.WithValue(ctx, ctxKeyToken, token)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireAdmin guards instance-management routes; it must sit inside
// RequireAuth so the user is already on the context. The role is read from
// the per-request user row, so a demotion takes effect immediately.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user := UserFrom(r.Context()); user == nil || user.Role != auth.RoleAdmin {
			writeError(w, http.StatusForbidden, codeForbidden, "admin role required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireFresh gates sudo-mode endpoints: the session must have proven the
// user's identity within the reauth window. It must sit inside RequireAuth so
// the session is already on the context. Clients recover from the 403 by
// confirming identity at POST /v1/auth/reauth and retrying.
func RequireFresh(svc *auth.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if sess := SessionFrom(r.Context()); sess == nil || !svc.IsSessionFresh(sess) {
				writeError(w, http.StatusForbidden, codeReauthRequired, "recent authentication required")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// bearerToken extracts the token from "Authorization: Bearer <token>",
// matching the scheme case-insensitively per RFC 9110.
func bearerToken(r *http.Request) (string, bool) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return "", false
	}
	scheme, rest, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	rest = strings.TrimSpace(rest)
	return rest, rest != ""
}

// requestLogger emits one slog line per request. It sits inside RequestID so
// the id is available, and outside the handlers so panics recovered further in
// still get logged with a 500 status by Recoverer.
func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		start := time.Now()
		next.ServeHTTP(ww, r)
		slog.InfoContext(r.Context(), "http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"bytes", ww.BytesWritten(),
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", middleware.GetReqID(r.Context()),
		)
	})
}

// InstanceHeader carries the installation identity on every response,
// including errors: the value is minted with the database (the schema baseline)
// and changes exactly when a cluster is uninstalled and reinstalled. Clients
// pin it per remote to tell a reinstalled cluster apart from an expired
// session; internal/client owns the pinning side. This is a convenience
// signal, not a security boundary: server authentication remains TLS's job.
const InstanceHeader = "Skali-Instance"

// VersionHeader carries the daemon build version on every response, so
// clients can diagnose version skew pre-auth and on failures (the meta
// endpoint needs a session). Diagnostics only: compatibility gates stay on
// explicit signals (error codes, capabilities), never on comparing versions.
const VersionHeader = "Skali-Version"

// platformHeaders stamps the installation identity and daemon version onto
// every response.
func platformHeaders(instanceID, version string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if instanceID != "" {
				w.Header().Set(InstanceHeader, instanceID)
			}
			if version != "" {
				w.Header().Set(VersionHeader, version)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// realIP accepts forwarding headers only from a trusted socket peer. Walk
// X-Forwarded-For from the nearest hop to the first untrusted address; entries
// beyond that address were supplied by an untrusted caller. Invalid input
// falls back to the socket peer. X-Real-IP is for proxies that overwrite it.
func realIP(trust func(netip.Addr) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			peer, err := netip.ParseAddr(clientIP(r))
			if err == nil && trust != nil && trust(peer.Unmap()) {
				if values, present := r.Header["X-Forwarded-For"]; present {
					parts := strings.Split(strings.Join(values, ","), ",")
					addresses := make([]netip.Addr, len(parts))
					valid := true
					for i, part := range parts {
						addresses[i], err = netip.ParseAddr(strings.TrimSpace(part))
						if err != nil {
							valid = false
							break
						}
					}
					if valid {
						for i := len(addresses) - 1; i >= 0; i-- {
							peer = addresses[i].Unmap()
							if !trust(peer) {
								break
							}
						}
						r.RemoteAddr = peer.String()
					}
				} else if values := r.Header.Values("X-Real-IP"); len(values) == 1 {
					if ip, err := netip.ParseAddr(strings.TrimSpace(values[0])); err == nil {
						r.RemoteAddr = ip.Unmap().String()
					}
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// clientIP returns the request's client address without the port; realIP has
// already resolved proxy headers into RemoteAddr.
func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
