package reconcile

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/artifactstore"
	"github.com/Hinkolas/skali/internal/deploy"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/store"
)

// deployChanged promotes a second revision (different image tag) of the
// given manifest on top of whatever the fixture deployed before.
func (f *kernelFixture) deployChanged(t *testing.T, base string) *deploy.ExecuteResult {
	t.Helper()
	ctx := context.Background()
	projects := project.New(f.st)
	draft, err := projects.GetDraft(ctx, f.projectID)
	require.NoError(t, err)
	submitted, err := projects.SubmitDraft(ctx, f.projectID, project.DraftSubmission{
		Source:          []byte(strings.Replace(base, ":1.0.0", ":2.0.0", 1)),
		Format:          "yaml",
		ExpectedVersion: draft.Version,
	})
	require.NoError(t, err)
	row, err := f.st.GetDefinitionVersionByHash(ctx, store.GetDefinitionVersionByHashParams{
		ProjectID: f.projectID, DefinitionHash: submitted.Hash,
	})
	require.NoError(t, err)
	result, err := f.deploy.Execute(ctx, deploy.ExecuteInput{
		ProjectID:           f.projectID,
		EnvironmentID:       f.environmentID,
		DefinitionVersionID: row.ID,
		Resolver:            &artifactstore.Fake{Store: artifactstore.New(f.st), ProjectID: f.projectID},
		Journal:             f.journal,
		Actor:               "tester",
	})
	require.NoError(t, err)
	return result
}

// A deployment run in its artifact window is never adopted: while the
// deployment row is preparing, the run belongs to the build client, and a
// reconcile pass converging a stale unhealthy target must not deadline-fail
// it (the incident shape: a wedged prior target plus a slow build).
func TestPreparingDeploymentRunNotAdopted(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Nanosecond})
	ctx := context.Background()

	// Revision A promotes but never becomes healthy; the operator cancelled
	// its run, leaving a stale unhealthy target behind.
	first := f.executeDeployment(t)
	f.fake.SetFresh()
	f.fake.SetWorkload(f.environmentID, f.namespace, "app-demo-web-714832ea87e5bc991f3f11667354c6c3", "web", "",
		module.WorkloadStatus{Desired: 1, Ready: 0, Updated: 1})
	require.NoError(t, f.journal.FinishRun(ctx, first.RunID, journal.RunCancelled))

	// A second deployment opens its artifact window: the run is running
	// while the client builds, the deployment row stays preparing.
	clientRun, err := f.journal.CreateRun(ctx, journal.RunInput{
		Kind:          "deployment",
		ProjectID:     f.projectID,
		EnvironmentID: f.environmentID,
		Actor:         "tester",
	})
	require.NoError(t, err)
	require.NoError(t, f.journal.StartRun(ctx, clientRun.ID))
	revision, err := f.st.GetRevisionByID(ctx, first.RevisionID)
	require.NoError(t, err)
	deployment, err := f.st.CreateDeployment(ctx, store.CreateDeploymentParams{
		ID:                  uuid.New(),
		ProjectID:           f.projectID,
		EnvironmentID:       f.environmentID,
		DefinitionVersionID: revision.DefinitionVersionID,
		RunID:               &clientRun.ID,
		Actor:               "tester",
		BuildExecutor:       "local",
		Actions:             []byte("[]"),
	})
	require.NoError(t, err)

	before := f.target(t)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)

	run, err := f.st.GetRunByID(ctx, clientRun.ID)
	require.NoError(t, err)
	require.Equal(t, "running", run.Status,
		"a preparing deployment's run must not be adopted or deadline-failed")
	require.Equal(t, *before.TargetRevisionID, *f.target(t).TargetRevisionID,
		"no fallback fires during the artifact window")
	require.Empty(t, f.pendingRuns(t),
		"a pass with no run to journal into strands none: the row could never "+
			"start, nothing would finish it, and retention reclaims only terminal runs")

	// Promote flips the boundary: the same run is adopted and the stale
	// unhealthy target now fails it under the deadline.
	require.NoError(t, f.st.SetDeploymentStatus(ctx, store.SetDeploymentStatusParams{
		ID: deployment.ID, Status: string(deploy.DeploymentPromoted),
	}))
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	run, err = f.st.GetRunByID(ctx, clientRun.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", run.Status)
}

// Section 8.4 automatic fallback: when a promoted revision misses its
// rollout deadline and a previously active revision exists, the run fails
// and the target returns to the active revision. Contrast with
// TestReconcileDeadlineFailsRunKeepsTarget, where the first deployment has
// nothing to fall back to.
func TestDeadlineFallbackToActive(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()

	// Revision A rolls out and activates.
	first := f.executeDeployment(t)
	f.fake.SetFresh()
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	f.markHealthy(t)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	target := f.target(t)
	require.NotNil(t, target.ActiveRevisionID)
	require.Equal(t, first.RevisionID, *target.ActiveRevisionID)

	// Revision B promotes but never becomes healthy; the deadline is
	// effectively immediate.
	second := f.deployChanged(t, kernelManifest)
	require.NotEqual(t, first.RevisionID, second.RevisionID)
	f.kernel.cfg.RolloutDeadline = time.Nanosecond
	f.fake.SetWorkload(f.environmentID, f.namespace, "app-demo-web-714832ea87e5bc991f3f11667354c6c3", "web", "",
		module.WorkloadStatus{Desired: 1, Ready: 0, Updated: 1})

	requeue, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Zero(t, requeue)

	run, err := f.st.GetRunByID(ctx, second.RunID)
	require.NoError(t, err)
	require.Equal(t, "failed", run.Status)

	target = f.target(t)
	require.Equal(t, first.RevisionID, *target.TargetRevisionID, "the target returns to the active revision")
	require.Equal(t, first.RevisionID, *target.ActiveRevisionID)

	// The fallback is a target write like any other: reconciliation simply
	// converges on revision A again, and a repeated pass stays idle.
	f.markHealthy(t)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	target = f.target(t)
	require.Equal(t, first.RevisionID, *target.ActiveRevisionID)
}

// drainQueue empties the kernel queue so a test can assert exactly what the
// next pass enqueues. The workqueue dedupes keys, so counting deltas would
// not work.
func (f *kernelFixture) drainQueue(t *testing.T) {
	t.Helper()
	for f.kernel.queue.Len() > 0 {
		item, shutdown := f.kernel.queue.Get()
		require.False(t, shutdown)
		f.kernel.queue.Done(item)
		f.kernel.queue.Forget(item)
	}
}

// pendingRuns lists the environment's runs that were created but never
// started.
func (f *kernelFixture) pendingRuns(t *testing.T) []store.Run {
	t.Helper()
	runs, err := f.st.ListRunsByEnvironment(context.Background(), &f.environmentID)
	require.NoError(t, err)
	var pending []store.Run
	for _, run := range runs {
		if run.Status == string(journal.RunPending) {
			pending = append(pending, run)
		}
	}
	return pending
}

// countOps counts the recorded cluster operations equal to op, including
// its forced-apply variant.
func countOps(ops []string, op string) int {
	count := 0
	for _, recorded := range ops {
		if recorded == op || recorded == op+" (forced)" {
			count++
		}
	}
	return count
}

// A release command failing with an active revision behind it: the run
// fails, the target returns to the active revision, and the fallback
// re-enters the queue so the next pass promptly re-renders the old
// revision (restoring its values Secret) instead of waiting for resync.
func TestReleaseFailureFallbackToActive(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()

	// Revision A (with a release command) rolls out and activates.
	first := f.executeDeploymentManifest(t, releaseManifest)
	f.fake.SetFresh()
	aJob := f.releaseJobName(t)
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	f.fake.SetReleaseJob(f.environmentID, f.namespace, aJob, "web",
		observe.JobStatus{Succeeded: true, Created: time.Now()})
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	f.markHealthy(t)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	target := f.target(t)
	require.NotNil(t, target.ActiveRevisionID)
	require.Equal(t, first.RevisionID, *target.ActiveRevisionID)

	// Revision B promotes; its release Job fails terminally for the
	// current attempt.
	second := f.deployChanged(t, releaseManifest)
	require.NotEqual(t, first.RevisionID, second.RevisionID)
	bJob := f.releaseJobName(t)
	require.NotEqual(t, aJob, bJob)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Contains(t, f.cluster.recorded(), "apply Job/"+f.namespace+"/"+bJob)
	f.fake.SetReleaseJob(f.environmentID, f.namespace, bJob, "web",
		observe.JobStatus{Failed: true, Reason: "BackoffLimitExceeded",
			Message: "Job has reached the specified backoff limit",
			Created: time.Now().Add(time.Minute)})

	f.drainQueue(t)
	secretApplies := countOps(f.cluster.recorded(), "apply Secret/"+f.namespace+"/skali-environment")
	workloadApplies := countOps(f.cluster.recorded(), "apply Deployment/"+f.namespace+"/app-demo-web-714832ea87e5bc991f3f11667354c6c3")

	requeue, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Zero(t, requeue)
	require.Equal(t, 1, f.kernel.queue.Len(),
		"the fallback must re-enter the queue for the prompt re-render")

	run, err := f.st.GetRunByID(ctx, second.RunID)
	require.NoError(t, err)
	require.Equal(t, "failed", run.Status)
	target = f.target(t)
	require.Equal(t, first.RevisionID, *target.TargetRevisionID, "the target returns to the active revision")
	require.Equal(t, first.RevisionID, *target.ActiveRevisionID)

	// The recovery pass re-renders revision A: its Secret and workload
	// re-apply, B's failed Job prunes, and no release Job re-runs (the
	// environment is no longer in flight).
	jobApplies := countOps(f.cluster.recorded(), "apply Job/"+f.namespace+"/"+aJob) +
		countOps(f.cluster.recorded(), "apply Job/"+f.namespace+"/"+bJob)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	ops := f.cluster.recorded()
	require.Greater(t, countOps(ops, "apply Secret/"+f.namespace+"/skali-environment"), secretApplies,
		"the old revision's values Secret must re-apply promptly")
	require.Greater(t, countOps(ops, "apply Deployment/"+f.namespace+"/app-demo-web-714832ea87e5bc991f3f11667354c6c3"), workloadApplies)
	require.Contains(t, ops, "delete Job/"+f.namespace+"/"+bJob)
	require.Equal(t, jobApplies,
		countOps(ops, "apply Job/"+f.namespace+"/"+aJob)+countOps(ops, "apply Job/"+f.namespace+"/"+bJob),
		"a converged environment never re-creates release Jobs")

	// Health recovers on revision A and the environment settles.
	f.markHealthy(t)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, first.RevisionID, *f.target(t).ActiveRevisionID)
}

// A rollback run is adopted exactly like a deployment run: it owns the
// rollout parent step, the kernel rolls the stored revision out under it,
// and activation finishes it.
func TestRollbackRunAdoptedAndActivates(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()

	// Revision A activates, then revision B activates on top.
	first := f.executeDeployment(t)
	f.fake.SetFresh()
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	f.markHealthy(t)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	second := f.deployChanged(t, kernelManifest)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	f.markHealthy(t)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, second.RevisionID, *f.target(t).ActiveRevisionID)

	// Roll back to revision A: the run stays running for the kernel.
	result, err := f.deploy.Rollback(ctx, deploy.RollbackInput{
		EnvironmentID: f.environmentID, RevisionID: first.RevisionID,
		Actor: "tester", Journal: f.journal,
	})
	require.NoError(t, err)
	run, err := f.st.GetRunByID(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "rollback", run.Kind)
	require.Equal(t, "running", run.Status)

	// The kernel adopts the run, re-applies revision A, and activation on
	// health concludes it.
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	f.markHealthy(t)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	run, err = f.st.GetRunByID(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", run.Status)
	target := f.target(t)
	require.Equal(t, first.RevisionID, *target.ActiveRevisionID)
	keys := f.runStepStatuses(t, result.RunID)
	require.Equal(t, "succeeded", keys["promote"])
	require.Equal(t, "succeeded", keys["rollout"])
	require.Equal(t, "succeeded", keys["activate"])
}

// A rollback missing its deadline gets the same safety net as a deployment:
// the run fails and the target returns to the still-active revision it
// rolled back from.
func TestRollbackDeadlineFallbackToActive(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()

	first := f.executeDeployment(t)
	f.fake.SetFresh()
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	f.markHealthy(t)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	second := f.deployChanged(t, kernelManifest)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	f.markHealthy(t)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, second.RevisionID, *f.target(t).ActiveRevisionID)

	// Roll back to A, which never becomes healthy; the deadline is
	// effectively immediate.
	result, err := f.deploy.Rollback(ctx, deploy.RollbackInput{
		EnvironmentID: f.environmentID, RevisionID: first.RevisionID,
		Actor: "tester", Journal: f.journal,
	})
	require.NoError(t, err)
	f.kernel.cfg.RolloutDeadline = time.Nanosecond
	f.fake.SetWorkload(f.environmentID, f.namespace, "app-demo-web-714832ea87e5bc991f3f11667354c6c3", "web", "",
		module.WorkloadStatus{Desired: 1, Ready: 0, Updated: 1})

	requeue, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Zero(t, requeue)

	run, err := f.st.GetRunByID(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "failed", run.Status)
	target := f.target(t)
	require.Equal(t, second.RevisionID, *target.TargetRevisionID,
		"the target returns to the revision the rollback left")
	require.Equal(t, second.RevisionID, *target.ActiveRevisionID)
}

// A backup or restore run holds the environment's single running-run slot
// while its controller moves data. The kernel must not adopt it: a
// converged pass's activate() would finish it succeeded mid-flight, and a
// teardown pass would do the same during a stop-first restore. The pass
// still reconciles; only the journaling is withheld.
func TestBackupRunNotAdoptedOrFinished(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Nanosecond})
	ctx := context.Background()

	// Converge the environment fully.
	f.executeDeployment(t)
	f.fake.SetFresh()
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	f.markHealthy(t)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)

	// A backup run claims the running slot, as the backup controller would.
	backupRun, err := f.journal.CreateRun(ctx, journal.RunInput{
		Kind:          "backup",
		ProjectID:     f.projectID,
		EnvironmentID: f.environmentID,
		Actor:         "tester",
	})
	require.NoError(t, err)
	require.NoError(t, f.journal.StartRun(ctx, backupRun.ID))

	// A converged pass (audit, resync, watch poke) must leave it running,
	// even under an expired rollout deadline.
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)

	run, err := f.st.GetRunByID(ctx, backupRun.ID)
	require.NoError(t, err)
	require.Equal(t, "running", run.Status,
		"the kernel must not adopt or finish a backup run")
	require.Empty(t, f.pendingRuns(t),
		"the pass's lazy run loses the StartRun race and is discarded, not stranded")
}
