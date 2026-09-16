package deploy

import (
	"context"
	"time"
)

// cleanupTimeout bounds the writes that must land even after the caller's
// context is gone: a client that disconnected mid-request, or a daemon that
// is shutting down.
const cleanupTimeout = 30 * time.Second

// detached returns a context that survives ctx's cancellation for
// failure-path writes. A cancelled request must never leave a run running
// or a deployment preparing; the deadline keeps a stuck database from
// pinning the goroutine forever.
func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
}
