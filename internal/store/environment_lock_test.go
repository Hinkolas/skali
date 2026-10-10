package store

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/testdb"
)

func lockStore(t *testing.T) *Store {
	t.Helper()
	st := NewStore(testdb.New(t))
	t.Cleanup(st.Close)
	return st
}

// waiters counts the sessions queued on an advisory lock.
func waiters(t *testing.T, st *Store) int {
	t.Helper()
	var count int
	require.NoError(t, st.Pool.QueryRow(context.Background(),
		"SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted").Scan(&count))
	return count
}

// Consecutive locks reuse one open connection, and each unlock leaves it
// clean for the next.
func TestLockEnvironmentReusesConnection(t *testing.T) {
	st := lockStore(t)
	ctx := context.Background()
	id := uuid.New()
	for range 3 {
		unlock, err := st.LockEnvironment(ctx, id)
		require.NoError(t, err)
		unlock()
	}
	unlock, err := st.LockEnvironmentWithin(ctx, uuid.New(), time.Second)
	require.NoError(t, err)
	unlock()
	require.EqualValues(t, 1, st.locks.Stat().NewConnsCount())
}

// A bounded wait answers busy once the holder kept the lock for all of it,
// and leaves its connection without the lock or the timeout.
func TestLockEnvironmentWithinAnswersBusy(t *testing.T) {
	st := lockStore(t)
	ctx := context.Background()
	id := uuid.New()
	unlock, err := st.LockEnvironment(ctx, id)
	require.NoError(t, err)
	defer unlock()

	started := time.Now()
	_, err = st.LockEnvironmentWithin(ctx, id, 200*time.Millisecond)
	require.ErrorIs(t, err, ErrEnvironmentBusy)
	require.GreaterOrEqual(t, time.Since(started), 200*time.Millisecond)
	require.Less(t, time.Since(started), 2*time.Second)

	other, err := st.LockEnvironmentWithin(ctx, uuid.New(), time.Second)
	require.NoError(t, err, "another environment is not busy")
	other()
	conn, err := st.locks.Acquire(ctx)
	require.NoError(t, err)
	defer conn.Release()
	var timeout string
	require.NoError(t, conn.QueryRow(ctx, "SHOW lock_timeout").Scan(&timeout))
	require.Equal(t, "0", timeout, "the bound reverts with its statement")
}

// Waiters queue in the server: a bounded waiter wakes as the holder
// unlocks, ahead of a pass that asked after it.
func TestLockEnvironmentWaitersQueueInOrder(t *testing.T) {
	st := lockStore(t)
	ctx := context.Background()
	id := uuid.New()
	unlock, err := st.LockEnvironment(ctx, id)
	require.NoError(t, err)

	var mu sync.Mutex
	var order []string
	var done sync.WaitGroup
	take := func(name string, lock func() (func(), error)) {
		done.Go(func() {
			release, err := lock()
			if err != nil {
				t.Errorf("%s: %v", name, err)
				return
			}
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			time.Sleep(50 * time.Millisecond)
			release()
		})
	}
	take("request", func() (func(), error) { return st.LockEnvironmentWithin(ctx, id, 5*time.Second) })
	require.Eventually(t, func() bool { return waiters(t, st) == 1 }, 5*time.Second, 10*time.Millisecond)
	take("pass", func() (func(), error) { return st.LockEnvironment(ctx, id) })
	require.Eventually(t, func() bool { return waiters(t, st) == 2 }, 5*time.Second, 10*time.Millisecond)

	released := time.Now()
	unlock()
	done.Wait()
	require.Equal(t, []string{"request", "pass"}, order)
	require.Less(t, time.Since(released), 2*time.Second)
}

// A wait its context ends leaves no lock behind.
func TestLockEnvironmentCancelledWaitReleases(t *testing.T) {
	st := lockStore(t)
	ctx := context.Background()
	id := uuid.New()
	unlock, err := st.LockEnvironment(ctx, id)
	require.NoError(t, err)

	cancelled, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	_, err = st.LockEnvironment(cancelled, id)
	require.Error(t, err)
	unlock()

	again, err := st.LockEnvironmentWithin(ctx, id, 5*time.Second)
	require.NoError(t, err)
	again()
}
