package api

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/Hinkolas/skali/internal/auth"
)

// RequireAuth guards a route group with bearer-token authentication and puts
// the resolved user, session, and raw token on the request context. It must
// wrap only the protected group — never the router root, or login locks
// itself out.
func RequireAuth(a auth.Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := bearerToken(r)
			if !ok {
				writeError(w, http.StatusUnauthorized, codeInvalidToken, "missing or malformed Authorization header")
				return
			}
			user, sess, err := a.Authenticate(r.Context(), token)
			if err != nil {
				writeAuthError(r.Context(), w, err)
				return
			}
			ctx := context.WithValue(r.Context(), ctxKeyUser, user)
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
	scheme, rest, ok := strings.Cut(r.Header.Get("Authorization"), " ")
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
// including errors: the value is minted with the database (migration 00015)
// and changes exactly when a cluster is uninstalled and reinstalled. Clients
// pin it per remote to tell a reinstalled cluster apart from an expired
// session; internal/client owns the pinning side. This is a convenience
// signal, not a security boundary: server authentication remains TLS's job.
const InstanceHeader = "Skali-Instance"

// instanceHeader stamps the installation identity onto every response.
func instanceHeader(id string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set(InstanceHeader, id)
			next.ServeHTTP(w, r)
		})
	}
}

// realIP folds the proxy-reported client address into r.RemoteAddr: X-Real-IP
// first, else the rightmost X-Forwarded-For entry — the one appended by the
// nearest hop. These headers are trusted because the daemon is documented to
// run behind the operator's reverse proxy or the SvelteKit BFF. (chi's RealIP
// middleware is deprecated for using the leftmost, client-spoofable value.)
func realIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" {
			r.RemoteAddr = ip
		} else if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
				r.RemoteAddr = ip
			}
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP returns the request's client address without the port; realIP has
// already resolved proxy headers into RemoteAddr.
func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
