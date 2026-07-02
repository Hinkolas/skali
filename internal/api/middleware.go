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
