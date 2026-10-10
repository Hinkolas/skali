package store

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Hinkolas/skali/internal/workstats"
)

// ErrEnvironmentBusy is LockEnvironmentWithin's answer when the lock stayed
// held, by a reconcile pass or another change, for the whole wait.
var ErrEnvironmentBusy = errors.New("store: environment busy: a reconcile pass or another change held its lock")

// The lock pool keeps its connections open between passes, so a lock costs
// a round trip instead of a new connection. Every holder and every waiter
// holds one connection: the kernel's workers, the restore controller, and
// requests waiting on a pass. A connection idle for longer than
// lockPingAfter is checked before use; one used more recently is trusted,
// and if it died with the database, the caller gets the connection error.
const (
	lockConns     = 16
	lockConnsIdle = 5 * time.Minute
	lockPingAfter = 30 * time.Second
)

// lockPool returns the pool of connections that hold environment locks,
// apart from the query pool so workers holding locks cannot exhaust it.
func (s *Store) lockPool() (*pgxpool.Pool, error) {
	s.locksOnce.Do(func() {
		config := s.Pool.Config()
		config.ConnConfig.Tracer = nil
		config.MaxConns = lockConns
		config.MinConns = 0
		config.MaxConnIdleTime = lockConnsIdle
		config.ShouldPing = func(_ context.Context, params pgxpool.ShouldPingParams) bool {
			return params.IdleDuration > lockPingAfter
		}
		s.locks, s.locksErr = pgxpool.NewWithConfig(context.Background(), config)
	})
	return s.locks, s.locksErr
}

// LockEnvironment serializes target transitions and cluster mutations across
// processes, waiting as long as ctx allows. Kernel passes and background
// controllers take it this way; callers that answer someone use
// LockEnvironmentWithin. A pass in ctx is charged the connection and the
// wait apart from its statements.
func (s *Store) LockEnvironment(ctx context.Context, id uuid.UUID) (func(), error) {
	return s.lockEnvironment(ctx, id, 0)
}

// LockEnvironmentWithin is LockEnvironment bounded by wait: when the lock
// is still held after it, it returns ErrEnvironmentBusy instead of waiting
// on.
func (s *Store) LockEnvironmentWithin(ctx context.Context, id uuid.UUID, wait time.Duration) (func(), error) {
	return s.lockEnvironment(ctx, id, wait)
}

func (s *Store) lockEnvironment(ctx context.Context, id uuid.UUID, wait time.Duration) (func(), error) {
	pool, err := s.lockPool()
	if err != nil {
		return nil, err
	}
	started := time.Now()
	acquireCtx := ctx
	if wait > 0 {
		var cancel context.CancelFunc
		acquireCtx, cancel = context.WithTimeout(ctx, wait)
		defer cancel()
	}
	conn, err := pool.Acquire(acquireCtx)
	if err != nil {
		if wait > 0 && ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			return nil, ErrEnvironmentBusy
		}
		return nil, err
	}
	connected := time.Now()
	sum := sha256.Sum256(append([]byte("skali-environment:"), id[:]...))
	key := int64(binary.BigEndian.Uint64(sum[:8]))
	statement, args := "SELECT pg_advisory_lock($1)", []any{key}
	if wait > 0 {
		// lock_timeout bounds the wait in the server, where waiters queue
		// in order and wake the moment the holder unlocks. Set local, it
		// reverts with the statement; the subquery sets it before the lock
		// is requested.
		remaining := max(wait-connected.Sub(started), time.Millisecond)
		statement = "SELECT pg_advisory_lock($1) FROM (SELECT set_config('lock_timeout', $2, true) OFFSET 0) AS bounded"
		args = append(args, strconv.FormatInt(remaining.Milliseconds(), 10))
	}
	if _, err := conn.Exec(ctx, statement, args...); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "55P03" { // lock_not_available
			conn.Release()
			return nil, ErrEnvironmentBusy
		}
		discard(conn)
		return nil, err
	}
	workstats.PassFrom(ctx).AddLock(connected.Sub(started), time.Since(connected))
	return func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var unlocked bool
		if err := conn.QueryRow(cleanup, "SELECT pg_advisory_unlock($1)", key).Scan(&unlocked); err != nil || !unlocked {
			discard(conn)
			return
		}
		conn.Release()
	}, nil
}

// discard closes a lock connection instead of returning it to the pool:
// its session may still hold the lock, and closing it is the one sure
// release.
func discard(conn *pgxpool.Conn) {
	cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn.Hijack().Close(cleanup)
}
