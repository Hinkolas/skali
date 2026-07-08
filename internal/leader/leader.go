// Package leader elects the single active master through a Postgres session
// advisory lock. Every skalid master pointed at the same database competes
// for one lock; holding it IS leadership. There is no extra coordination
// service and no lease table: the lock lives in the database's session state,
// so it releases the instant the holder's connection dies and a parked
// standby takes over immediately. Everything the loops need to resume is
// already in Postgres — desired state, observed state, retry cursors — so a
// new leader simply starts the loops and converges.
package leader

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
)

// lockID is the one cluster-leader advisory lock key ("skalidL1" in hex).
// Advisory locks are per-database, so masters sharing DATABASE_URL contend
// and independent clusters never do.
const lockID int64 = 0x736b616c69644c31

const (
	defaultPingInterval = 5 * time.Second
	retryBackoffMin     = time.Second
	retryBackoffMax     = 30 * time.Second
)

// Lease acquires and holds cluster leadership. Construct with New, ask
// IsLeader for the current state, and hand Run the callback that spawns the
// leader-only loops.
type Lease struct {
	// connect is a seam: production connects by URL, tests inject a config
	// pointed at their ephemeral database.
	connect func(ctx context.Context) (*pgx.Conn, error)
	ping    time.Duration
	leader  atomic.Bool
}

func New(databaseURL string) *Lease {
	return &Lease{
		connect: func(ctx context.Context) (*pgx.Conn, error) {
			return pgx.Connect(ctx, databaseURL)
		},
		ping: defaultPingInterval,
	}
}

// IsLeader reports whether this process currently holds the lease.
func (l *Lease) IsLeader() bool { return l.leader.Load() }

// Run blocks until ctx ends. Each time the lock is acquired it calls run with
// a fresh leader-scoped context (run spawns its loops and returns); the
// context is canceled when leadership is lost or ctx ends. The callback must
// therefore be safe to invoke again on re-election.
func (l *Lease) Run(ctx context.Context, run func(leaderCtx context.Context)) {
	backoff := retryBackoffMin
	failing := false
	for {
		held, err := l.lead(ctx, run)
		if ctx.Err() != nil {
			return
		}
		if held {
			// We were the leader and lost the lease (lead already logged the
			// loss). Retry immediately: if the database merely blipped, this
			// re-acquires or parks behind whoever took over.
			backoff, failing = retryBackoffMin, false
			continue
		}
		// Never acquired: the database is unreachable. Transition-only noise.
		if !failing {
			slog.WarnContext(ctx, "leader lease unavailable, retrying", "err", err)
			failing = true
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff + rand.N(backoff/2)):
		}
		backoff = min(backoff*2, retryBackoffMax)
	}
}

// lead is one leadership attempt: connect, block on the lock, run the loops,
// then hold the lease by pinging the same session. held reports whether the
// lock was ever acquired. Deferred in LIFO order, losing the lease cancels
// the leader context before the connection closes and the lock frees — the
// loops are told to stop before a successor can start. In-flight operations
// may still overlap a new leader for a moment; every write they perform is an
// idempotent upsert, so overlap costs nothing but duplicate work.
func (l *Lease) lead(ctx context.Context, run func(context.Context)) (held bool, err error) {
	conn, err := l.connect(ctx)
	if err != nil {
		return false, err
	}
	// Background: ctx is likely already canceled when this runs.
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.Close(closeCtx)
	}()

	// Blocks until the lock is free: a standby parks here and takes over the
	// moment the leader's session dies.
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", lockID); err != nil {
		return false, err
	}
	slog.InfoContext(ctx, "leadership acquired")
	l.leader.Store(true)
	defer l.leader.Store(false)

	leaderCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	run(leaderCtx)

	ticker := time.NewTicker(l.ping)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case <-ticker.C:
			pingCtx, cancelPing := context.WithTimeout(ctx, l.ping)
			err := conn.Ping(pingCtx)
			cancelPing()
			if err != nil {
				// The session is gone (and with it the lock) or unreachable;
				// either way we must assume a successor. Close the connection
				// so a half-dead session can't keep holding the lock.
				slog.WarnContext(ctx, "leadership lost: lease session failed", "err", err)
				return true, err
			}
		}
	}
}
