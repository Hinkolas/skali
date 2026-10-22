package reconcile

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/module"
)

// requireNoRunningRun asserts the environment's operation slot is free.
func (f *kernelFixture) requireNoRunningRun(t *testing.T) {
	t.Helper()
	_, err := f.st.GetRunningRunByEnvironment(context.Background(), &f.environmentID)
	require.ErrorIs(t, err, pgx.ErrNoRows, "a run was left running")
}

// A pass that created its reconcile run and then fails on a path with no
// step of its own still concludes the run: a run left running would hold
// the environment's only operation slot and refuse every deployment.
func TestPassConcludesItsRunOnEarlyFailure(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	f.deployActive(t)

	// Drift: every object applies as a change again, so the pass creates
	// its run; then reading the live routes fails.
	f.cluster.mu.Lock()
	f.cluster.applied = map[string]bool{}
	f.cluster.mu.Unlock()
	_, err := f.st.Pool.Exec(ctx, `INSERT INTO hostname_claims (hostname, environment_id) VALUES ('old.example.com', $1)`,
		f.environmentID)
	require.NoError(t, err)
	f.kernel.deps.LiveRouteHosts = func(context.Context, uuid.UUID) (map[string]bool, error) {
		return nil, errors.New("route listing unavailable")
	}

	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.Error(t, err)
	f.requireNoRunningRun(t)
	runs, err := f.journal.ListRuns(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, "reconcile", runs[0].Kind)
	require.Equal(t, "failed", runs[0].Status)
	require.NotNil(t, runs[0].Failure)
	require.Contains(t, *runs[0].Failure, "route listing unavailable")
}

// A reconcile run an interrupted pass left running belongs to the kernel
// alone: the next pass adopts and concludes it, whether the environment is
// healthy or not, instead of leaving it to block deployments.
func TestPassConcludesReconcileRunLeftRunning(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	f.deployActive(t)

	left, err := f.journal.BeginRun(ctx, journal.RunInput{
		Kind: "reconcile", ProjectID: f.projectID, EnvironmentID: f.environmentID, Actor: actorReconcile,
	})
	require.NoError(t, err)
	f.setWebWorkload(t, "", module.WorkloadStatus{Desired: 1, Ready: 0, Updated: 1})

	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	f.requireNoRunningRun(t)
	run, err := f.st.GetRunByID(ctx, left.ID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", run.Status)
}
