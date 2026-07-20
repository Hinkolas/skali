package buildstore

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/lifecycle"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/testdb"
)

func TestStatusMachine(t *testing.T) {
	t.Parallel()
	require.NoError(t, lifecycle.Verify(Statuses))
	require.Equal(t, StatusQueued, Statuses.Initial())

	// The queue path belongs to workers; an expired claim requeues.
	require.True(t, Statuses.Can(StatusQueued, StatusClaimed))
	require.True(t, Statuses.Can(StatusClaimed, StatusQueued))
	require.True(t, Statuses.Can(StatusClaimed, StatusRunning))
	require.False(t, Statuses.Can(StatusQueued, StatusRunning))

	// Terminal outcomes are final.
	require.True(t, Statuses.Terminal(StatusSucceeded))
	require.True(t, Statuses.Terminal(StatusFailed))
	require.True(t, Statuses.Terminal(StatusCancelled))
	require.False(t, Statuses.Can(StatusSucceeded, StatusRunning))
}

func TestLocalBuildLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc := New(store.NewStore(testdb.New(t)))

	created, err := svc.CreateLocal(ctx, Local{
		Application: "web",
		Platform:    "linux/arm64",
		ContextHash: "tree-and-config",
		ConfigHash:  "config-only",
		StepKey:     "artifacts.web.build",
	})
	require.NoError(t, err)
	require.Equal(t, "local", created.Origin)
	require.Equal(t, string(StatusRunning), created.Status)
	require.NotNil(t, created.StartedAt)
	require.NotNil(t, created.HeartbeatAt)

	alive, err := svc.Heartbeat(ctx, created.ID)
	require.NoError(t, err)
	require.True(t, alive)

	// A running local build is visible to the staleness sweep once its
	// heartbeat ages past the cutoff.
	stale, err := svc.ListStaleLocal(ctx, time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, stale, 1)
	require.Equal(t, created.ID, stale[0].ID)

	// Finishing requires a terminal status and consults the machine.
	require.ErrorIs(t, svc.Finish(ctx, created.ID, StatusRunning), ErrInvalidTransition)
	require.NoError(t, svc.Finish(ctx, created.ID, StatusSucceeded))
	got, err := svc.Get(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, string(StatusSucceeded), got.Status)
	require.NotNil(t, got.FinishedAt)

	// Terminal builds neither heartbeat nor re-finish nor sweep.
	alive, err = svc.Heartbeat(ctx, created.ID)
	require.NoError(t, err)
	require.False(t, alive)
	require.ErrorIs(t, svc.Finish(ctx, created.ID, StatusFailed), ErrInvalidTransition)
	stale, err = svc.ListStaleLocal(ctx, time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.Empty(t, stale)

	require.ErrorIs(t, svc.Finish(ctx, uuid.New(), StatusFailed), ErrNotFound)
	_, err = svc.Get(ctx, uuid.New())
	require.ErrorIs(t, err, ErrNotFound)
}
