package reconcile

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/observe"
)

// startRolloutAgo moves the target's rollout start into the past, as if
// the rollout had been running for that long.
func (f *kernelFixture) startRolloutAgo(t *testing.T, ago time.Duration) {
	t.Helper()
	_, err := f.st.Pool.Exec(context.Background(),
		`UPDATE environment_targets SET rollout_started_at = $2, updated_at = $2 WHERE environment_id = $1`,
		f.environmentID, time.Now().Add(-ago))
	require.NoError(t, err)
}

// A rollout whose run is gone still has a deadline: past it the target
// returns to the active revision, and a reconcile run of its own says why.
func TestDeadlineFallbackWithoutRun(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	first := f.deployActive(t)

	second := f.deployChanged(t, kernelManifest)
	// The run finished without undoing the rollout: the rollout goes on
	// with no run to adopt.
	require.NoError(t, f.journal.FinishRun(ctx, second.RunID, journal.RunCancelled))
	f.setWebWorkload(t, "", module.WorkloadStatus{Desired: 1, Ready: 0, Updated: 1})
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, second.RevisionID, *f.target(t).TargetRevisionID, "within its deadline the rollout goes on")

	f.startRolloutAgo(t, 2*time.Hour)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	target := f.target(t)
	require.Equal(t, first.RevisionID, *target.TargetRevisionID, "the target returns to the active revision")

	runs, err := f.journal.ListRuns(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, "reconcile", runs[0].Kind)
	require.Equal(t, "failed", runs[0].Status)
	require.NotNil(t, runs[0].Failure)
	require.Contains(t, *runs[0].Failure, "rollout deadline exceeded")
	require.Equal(t, "failed", f.runStepStatuses(t, runs[0].ID)["verify"])
}

// A first deployment whose run is gone has nothing to fall back to and no
// run to fail: past its deadline the pass changes nothing and records
// nothing, and a late recovery still activates.
func TestDeadlineWithoutRunOrFallbackKeepsTarget(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Nanosecond})
	ctx := context.Background()
	result := f.executeDeployment(t)
	require.NoError(t, f.journal.FinishRun(ctx, result.RunID, journal.RunCancelled))
	f.fake.SetFresh()
	f.setWebWorkload(t, "", module.WorkloadStatus{Desired: 1, Ready: 0, Updated: 1})

	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, result.RevisionID, *f.target(t).TargetRevisionID)
	runs, err := f.journal.ListRuns(ctx, f.environmentID)
	require.NoError(t, err)
	for _, run := range runs {
		require.NotEqual(t, "failed", run.Status, "nothing failed: run %s (%s)", run.ID, run.Kind)
	}

	f.markHealthy(t)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, result.RevisionID, *f.target(t).ActiveRevisionID)
}

// The release budget counts from the release Job's creation, not from the
// promotion: a migration that starts late still gets its whole timeout,
// and the rollout after it its whole budget. Once the Job's window and the
// budget after it have passed, the deadline holds.
func TestDeadlineCountsReleaseFromJobCreation(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: 10 * time.Minute})
	ctx := context.Background()
	result := f.executeDeploymentManifest(t, releaseManifest)
	f.fake.SetFresh()
	jobName := f.releaseJobName(t)
	// Far longer ago than the budget and the release timeout together.
	f.startRolloutAgo(t, time.Hour)

	f.fake.SetReleaseJob(f.environmentID, f.namespace, jobName, "web",
		observe.JobStatus{Created: time.Now(), Promotion: f.promotion(t)})
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	run, err := f.st.GetRunByID(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "running", run.Status, "a release Job created just now runs within its own timeout")

	// The Job succeeded right away; the workload then has the budget.
	f.fake.SetReleaseJob(f.environmentID, f.namespace, jobName, "web",
		observe.JobStatus{Succeeded: true, Created: time.Now(), Promotion: f.promotion(t)})
	f.setWebWorkload(t, "", module.WorkloadStatus{Desired: 1, Ready: 0, Updated: 1})
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	run, err = f.st.GetRunByID(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "running", run.Status)

	// The Job's timeout and the budget after it have passed.
	f.fake.SetReleaseJob(f.environmentID, f.namespace, jobName, "web",
		observe.JobStatus{Succeeded: true, Created: time.Now().Add(-30 * time.Minute), Promotion: f.promotion(t)})
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	run, err = f.st.GetRunByID(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "failed", run.Status)
}

// The pass that moves traffic to the new color never falls back in the
// same breath: the new color gets one confirmation pass, even past the
// deadline. The pass after it enforces the deadline again.
func TestDeadlineSparesTheSwitchingPass(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour, RetireDrain: time.Hour})
	ctx := context.Background()
	oldColor := f.deployActiveWithService(t)
	first := *f.target(t).ActiveRevisionID

	second := f.executeDeploymentManifest(t, kernelManifestV2)
	newColor := f.webColor(t)
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, oldColor, f.appliedServiceColor(t), "traffic holds while the new color starts")

	// The new color becomes available only after the deadline passed.
	f.startRolloutAgo(t, 2*time.Hour)
	f.setWebWorkload(t, "", module.WorkloadStatus{Desired: 1, Ready: 1, Updated: 1, Available: 1})
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, newColor, f.appliedServiceColor(t), "the switch happened")
	require.Equal(t, second.RevisionID, *f.target(t).TargetRevisionID, "no fallback in the switching pass")
	run, err := f.st.GetRunByID(ctx, second.RunID)
	require.NoError(t, err)
	require.Equal(t, "running", run.Status)

	// The confirmation pass sees the new color fail: the deadline holds.
	f.observeService(t)
	f.setWebWorkload(t, "", module.WorkloadStatus{Desired: 1, Ready: 0, Updated: 1})
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, first, *f.target(t).TargetRevisionID, "the confirmation pass falls back")
	run, err = f.st.GetRunByID(ctx, second.RunID)
	require.NoError(t, err)
	require.Equal(t, "failed", run.Status)
}
