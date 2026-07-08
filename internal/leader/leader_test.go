package leader

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/testdb"
)

// testLease builds a fast-interval lease connected to the test's ephemeral
// database. The pool's ConnString still names the admin database (testdb
// overrides the database on the config, not the string), so connect from the
// ConnConfig — advisory locks are per-database and parallel test runs must
// not contend on a shared one.
func testLease(pool *pgxpool.Pool) *Lease {
	cfg := pool.Config().ConnConfig.Copy()
	return &Lease{
		connect: func(ctx context.Context) (*pgx.Conn, error) {
			return pgx.ConnectConfig(ctx, cfg)
		},
		ping: 25 * time.Millisecond,
	}
}

// leaderState tracks how often a lease's run callback fired and the latest
// leader context it received.
type leaderState struct {
	runs atomic.Int32
	ctx  atomic.Value // context.Context
}

func (s *leaderState) run(ctx context.Context) {
	s.ctx.Store(ctx)
	s.runs.Add(1)
}

func (s *leaderState) leaderCtx() context.Context {
	ctx, _ := s.ctx.Load().(context.Context)
	return ctx
}

func TestLeaseHandoverOnShutdown(t *testing.T) {
	pool := testdb.New(t)
	a, b := testLease(pool), testLease(pool)
	var aState, bState leaderState

	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	go a.Run(ctxA, aState.run)
	require.Eventually(t, a.IsLeader, 5*time.Second, 10*time.Millisecond)
	require.EqualValues(t, 1, aState.runs.Load())

	go b.Run(t.Context(), bState.run)
	// B parks on the lock: never leader, callback never fired.
	time.Sleep(150 * time.Millisecond)
	require.False(t, b.IsLeader())
	require.Zero(t, bState.runs.Load())

	// A shuts down: its session closes, the lock frees, B takes over.
	cancelA()
	require.Eventually(t, b.IsLeader, 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return !a.IsLeader() }, 5*time.Second, 10*time.Millisecond)
	require.EqualValues(t, 1, bState.runs.Load())

	// A's leader context died with its leadership.
	select {
	case <-aState.leaderCtx().Done():
	default:
		t.Fatal("old leader's context should be canceled")
	}
}

func TestLeaseRecoversFromKilledSession(t *testing.T) {
	pool := testdb.New(t)
	a := testLease(pool)
	var state leaderState

	go a.Run(t.Context(), state.run)
	require.Eventually(t, a.IsLeader, 5*time.Second, 10*time.Millisecond)
	firstCtx := state.leaderCtx()

	// Kill the lease session server-side: the next ping fails, leadership
	// drops (canceling the leader context), then Run re-acquires.
	terminateLockHolder(t, pool)
	require.Eventually(t, func() bool {
		return firstCtx.Err() != nil
	}, 5*time.Second, 10*time.Millisecond, "leader context should cancel on session loss")
	require.Eventually(t, func() bool {
		return a.IsLeader() && state.runs.Load() == 2
	}, 10*time.Second, 10*time.Millisecond, "lease should re-acquire with a fresh callback")
	require.NotSame(t, firstCtx, state.leaderCtx())
}

// terminateLockHolder kills the backend holding the cluster-leader advisory
// lock in this test's database.
func terminateLockHolder(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	tag, err := pool.Exec(context.Background(), `
		SELECT pg_terminate_backend(pid) FROM pg_locks
		WHERE locktype = 'advisory' AND granted
		  AND (classid::bigint << 32) + objid::bigint = $1
		  AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`,
		lockID)
	require.NoError(t, err)
	require.EqualValues(t, 1, tag.RowsAffected(), "exactly one session should hold the lease")
}
