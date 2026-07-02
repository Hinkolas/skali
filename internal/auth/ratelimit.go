package auth

import (
	"sync"
	"time"
)

// rateLimiter is a fixed-window counter, in memory. It is deliberately the
// simplest thing that blunts online brute force on the auth endpoints.
// Caveat: per-process state — if skalid ever scales horizontally, the
// effective limit multiplies by the instance count and this needs to move to a
// shared store (Postgres).
type rateLimiter struct {
	mu      sync.Mutex
	entries map[string]*rlEntry
	now     func() time.Time
}

type rlEntry struct {
	windowStart time.Time
	count       int
}

// pruneThreshold bounds memory: once the map grows past this, expired windows
// are swept during the next allow call.
const pruneThreshold = 16384

func newRateLimiter(now func() time.Time) *rateLimiter {
	return &rateLimiter{entries: make(map[string]*rlEntry), now: now}
}

// allow records an attempt under key and reports whether it is within limit
// for the current window.
func (l *rateLimiter) allow(key string, limit int, window time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	e := l.entries[key]
	if e == nil || now.Sub(e.windowStart) >= window {
		if len(l.entries) >= pruneThreshold {
			l.prune(now, window)
		}
		l.entries[key] = &rlEntry{windowStart: now, count: 1}
		return limit >= 1
	}
	e.count++
	return e.count <= limit
}

// prune removes entries whose window has passed. Caller holds l.mu.
func (l *rateLimiter) prune(now time.Time, window time.Duration) {
	for k, e := range l.entries {
		if now.Sub(e.windowStart) >= window {
			delete(l.entries, k)
		}
	}
}
