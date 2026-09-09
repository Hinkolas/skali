package api

import (
	"context"

	"github.com/Hinkolas/skali/internal/store"
)

type ctxKey int

const (
	ctxKeyUser ctxKey = iota
	ctxKeySession
	ctxKeyToken
	ctxKeyCookieAuth
	ctxKeyBrowserPolicy
)

// UserFrom returns the authenticated user; nil outside RequireAuth.
func UserFrom(ctx context.Context) *store.User {
	u, _ := ctx.Value(ctxKeyUser).(*store.User)
	return u
}

// SessionFrom returns the authenticated session; nil outside RequireAuth.
func SessionFrom(ctx context.Context) *store.Session {
	s, _ := ctx.Value(ctxKeySession).(*store.Session)
	return s
}

// tokenFrom returns the raw bearer token the request authenticated with.
func tokenFrom(ctx context.Context) string {
	t, _ := ctx.Value(ctxKeyToken).(string)
	return t
}
