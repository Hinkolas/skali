package reconcile

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/module"
)

// deployHealthy runs the fixture through a full rollout: promote, one pass
// that applies, health arriving through observation, and one pass that
// activates. The kernel has recorded a healthy verdict afterwards.
func (f *kernelFixture) deployHealthy(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	f.executeDeployment(t)
	f.fake.SetFresh()
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	f.markHealthy(t)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
}

func TestPassRecordsEnvironmentHealth(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()

	_, ok := f.kernel.EnvironmentHealth(f.environmentID)
	require.False(t, ok, "nothing evaluated before the first pass")

	started := time.Now()
	f.executeDeployment(t)
	f.fake.SetFresh()
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)

	// The first pass applied and evaluated a workload that is not ready yet.
	verdict, ok := f.kernel.EnvironmentHealth(f.environmentID)
	require.True(t, ok)
	require.NotEqual(t, module.HealthHealthy, verdict.Health)
	require.False(t, verdict.EvaluatedAt.Before(started))

	f.markHealthy(t)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	verdict, ok = f.kernel.EnvironmentHealth(f.environmentID)
	require.True(t, ok)
	require.Equal(t, module.HealthHealthy, verdict.Health)

	// The batch read answers only for environments with a verdict.
	batch := f.kernel.EnvironmentHealths([]uuid.UUID{f.environmentID, uuid.New()})
	require.Len(t, batch, 1)
	require.Equal(t, module.HealthHealthy, batch[f.environmentID].Health)
}

func TestEnvironmentHealthFoldsStaleObservation(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	f.deployHealthy(t)

	before, ok := f.kernel.EnvironmentHealth(f.environmentID)
	require.True(t, ok)
	require.Equal(t, module.HealthHealthy, before.Health)

	// A stale watch changes health with no pass; the read folds it.
	f.fake.SetStale()
	stale, ok := f.kernel.EnvironmentHealth(f.environmentID)
	require.True(t, ok)
	require.Equal(t, module.HealthUnknown, stale.Health)
	require.Equal(t, before.EvaluatedAt, stale.EvaluatedAt, "the last real verdict's time stands")

	f.fake.SetFresh()
	fresh, ok := f.kernel.EnvironmentHealth(f.environmentID)
	require.True(t, ok)
	require.Equal(t, module.HealthHealthy, fresh.Health)
}

func TestEnvironmentHealthEvictedOnTeardown(t *testing.T) {
	t.Parallel()
	for _, purge := range []bool{false, true} {
		name := "down"
		if purge {
			name = "purge"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
			ctx := context.Background()
			f.deployHealthy(t)

			_, err := f.deploy.Teardown(ctx, f.environmentID, purge, f.journal, "tester")
			require.NoError(t, err)
			_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
			require.NoError(t, err)

			_, ok := f.kernel.EnvironmentHealth(f.environmentID)
			require.False(t, ok, "a torn-down environment has nothing to be healthy")
		})
	}
}

func TestEnvironmentHealthEvictedWhenRowGone(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	f.deployHealthy(t)

	// A raw delete removes the rows without a teardown pass.
	_, err := f.st.DeleteEnvironmentByID(ctx, f.environmentID)
	require.NoError(t, err)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)

	_, ok := f.kernel.EnvironmentHealth(f.environmentID)
	require.False(t, ok)
}

func TestAuditSweepsForgottenEnvironments(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	f.deployHealthy(t)

	_, err := f.st.DeleteEnvironmentByID(ctx, f.environmentID)
	require.NoError(t, err)
	f.kernel.audit(ctx)

	_, ok := f.kernel.EnvironmentHealth(f.environmentID)
	require.False(t, ok, "the audit retires verdicts of environments whose rows vanished")

	// A verdict recorded after the audit read its set survives the sweep:
	// its environment may have been created since.
	late := uuid.New()
	listedAt := time.Now().Add(-time.Minute)
	f.kernel.recordHealth(late, nil, time.Now())
	f.kernel.sweepHealth(map[uuid.UUID]bool{}, listedAt)
	_, ok = f.kernel.EnvironmentHealth(late)
	require.True(t, ok)
}

func TestEnvironmentHealthNotRecordedWithoutTarget(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{})
	f.fake.SetFresh()
	_, err := f.kernel.reconcileEnvironment(context.Background(), f.environmentID)
	require.NoError(t, err)
	_, ok := f.kernel.EnvironmentHealth(f.environmentID)
	require.False(t, ok, "an environment that never deployed has no verdict")
}

func TestRestartedKernelReportsMiss(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	f.deployHealthy(t)

	restarted := New(f.kernel.deps, f.kernel.cfg)
	_, ok := restarted.EnvironmentHealth(f.environmentID)
	require.False(t, ok, "verdicts are in-memory; a restart re-derives them")
}

func TestEnvironmentHealthWithoutObservation(t *testing.T) {
	t.Parallel()
	k := New(Deps{}, Config{})
	id := uuid.New()
	k.recordHealth(id, []ServiceStatus{{Health: module.HealthHealthy}}, time.Now())
	verdict, ok := k.EnvironmentHealth(id)
	require.True(t, ok)
	require.Equal(t, module.HealthUnknown, verdict.Health, "no observation means no trusted verdict")
}
