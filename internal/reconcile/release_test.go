package reconcile

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Hinkolas/skali/internal/artifactstore"
	"github.com/Hinkolas/skali/internal/deploy"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/kube"
	rendering "github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/observe"
)

const releaseManifest = `name: demo
applications:
  web:
    image: ghcr.io/example/web:1.0.0
    deployment:
      releaseCommand:
        command: ["/bin/migrate"]
    ports:
      http:
        port: 8080
        protocol: http
    environment:
      APP_DOMAIN: "${APP_DOMAIN}"
      SESSION_SECRET: "${SESSION_SECRET}"
`

// releaseJobName resolves the rendered per-revision Job name of the
// fixture's target revision.
func (f *kernelFixture) releaseJobName(t *testing.T) string {
	t.Helper()
	target := f.target(t)
	require.NotNil(t, target.TargetRevisionID)
	row, err := f.st.GetRevisionByID(context.Background(), *target.TargetRevisionID)
	require.NoError(t, err)
	return rendering.ReleaseJobName("demo", "web", row.Checksum)
}

// promotion is the promotion stamp of the fixture's current target.
func (f *kernelFixture) promotion(t *testing.T) string {
	t.Helper()
	return rendering.PromotionStamp(f.target(t).PromotedAt)
}

// appliedReleaseJob is the release Job the kernel last applied.
func (f *kernelFixture) appliedReleaseJob(t *testing.T, jobName string) *batchv1.Job {
	t.Helper()
	job, ok := f.cluster.lastApplied("Job/" + f.namespace + "/" + jobName).(*batchv1.Job)
	require.True(t, ok, "release Job %s was never applied", jobName)
	return job
}

// countOps counts the recorded cluster operations equal to op.
func (f *kernelFixture) countOps(op string) int {
	count := 0
	for _, recorded := range f.cluster.recorded() {
		if recorded == op {
			count++
		}
	}
	return count
}

func (f *kernelFixture) runStepStatuses(t *testing.T, runID uuid.UUID) map[string]string {
	t.Helper()
	tree, err := f.journal.RunTree(context.Background(), runID)
	require.NoError(t, err)
	keys := map[string]string{}
	var collect func(steps []*journal.TreeStep)
	collect = func(steps []*journal.TreeStep) {
		for _, step := range steps {
			keys[step.Step.Key] = step.Step.Status
			collect(step.Children)
		}
	}
	collect(tree.Steps)
	return keys
}

// The release command gates the workload: the Job is created first, the
// Deployment applies only after it succeeds, and the run's release step
// narrates the wait.
func TestReconcileReleaseCommandGatesApply(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	result := f.executeDeploymentManifest(t, releaseManifest)
	f.fake.SetFresh()
	jobName := f.releaseJobName(t)

	requeue, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, requeueHealthCheck, requeue)

	ops := f.cluster.recorded()
	require.Contains(t, ops, "apply Job/"+f.namespace+"/"+jobName)
	require.NotContains(t, ops, "apply Deployment/"+f.namespace+"/"+f.webDeploymentName(t),
		"the workload must not roll before the release command completes")
	require.Equal(t, f.promotion(t), f.appliedReleaseJob(t, jobName).Labels[rendering.LabelPromotion],
		"the Job is bound to the promotion it runs for")

	// Still running: another pass neither re-creates the Job nor applies.
	f.fake.SetReleaseJob(f.environmentID, f.namespace, jobName, "web",
		observe.JobStatus{Created: time.Now()})
	requeue, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, requeueHealthCheck, requeue)
	require.NotContains(t, f.cluster.recorded(), "apply Deployment/"+f.namespace+"/"+f.webDeploymentName(t))

	// Success unblocks the workload; health then activates the revision.
	f.fake.SetReleaseJob(f.environmentID, f.namespace, jobName, "web",
		observe.JobStatus{Succeeded: true, Created: time.Now()})
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Contains(t, f.cluster.recorded(), "apply Deployment/"+f.namespace+"/"+f.webDeploymentName(t))

	f.markHealthy(t)
	requeue, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Zero(t, requeue)
	target := f.target(t)
	require.NotNil(t, target.ActiveRevisionID)
	require.Equal(t, *target.TargetRevisionID, *target.ActiveRevisionID)

	keys := f.runStepStatuses(t, result.RunID)
	require.Equal(t, "succeeded", keys["release:web"])
	require.Equal(t, "succeeded", keys["apply:web"])
	require.Equal(t, "succeeded", keys["activate"])
}

// A release command failing for the current attempt fails the deployment
// run with the failure journaled on the release step; a first deployment
// keeps its target so a redeploy retries.
func TestReconcileReleaseFailureFailsRun(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	result := f.executeDeploymentManifest(t, releaseManifest)
	f.fake.SetFresh()
	jobName := f.releaseJobName(t)

	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)

	// The Job of this attempt (created after the promote) fails terminally.
	// A first deployment has nothing to fall back to: the run fails, the
	// target survives for a redeploy, and nothing requeues or re-enters the
	// queue (resync picks the environment up again).
	f.fake.SetReleaseJob(f.environmentID, f.namespace, jobName, "web",
		observe.JobStatus{Failed: true, Reason: "BackoffLimitExceeded",
			Message: "Job has reached the specified backoff limit",
			Created: time.Now().Add(time.Minute), Promotion: f.promotion(t)})
	f.drainQueue(t)
	requeue, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Zero(t, requeue)
	require.Zero(t, f.kernel.queue.Len(),
		"a first deployment has nothing to fall back to and must not requeue")

	run, err := f.st.GetRunByID(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "failed", run.Status)
	require.NotContains(t, f.cluster.recorded(), "apply Deployment/"+f.namespace+"/"+f.webDeploymentName(t))

	target := f.target(t)
	require.NotNil(t, target.TargetRevisionID, "a first deployment has nothing to fall back to")
	require.Nil(t, target.ActiveRevisionID)

	keys := f.runStepStatuses(t, result.RunID)
	require.Equal(t, "failed", keys["release:web"])
}

// A failed Job created before the current promote belongs to an earlier
// attempt: the pass deletes it and re-creates a fresh Job instead of
// failing the new rollout on stale state.
func TestReconcileReleaseRetriesStaleFailure(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	result := f.executeDeploymentManifest(t, releaseManifest)
	f.fake.SetFresh()
	jobName := f.releaseJobName(t)

	// A terminal failure that predates the promote.
	f.fake.SetReleaseJob(f.environmentID, f.namespace, jobName, "web",
		observe.JobStatus{Failed: true, Reason: "DeadlineExceeded",
			Created: time.Now().Add(-24 * time.Hour)})

	requeue, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, requeueHealthCheck, requeue)
	require.Contains(t, f.cluster.recorded(), "delete Job/"+f.namespace+"/"+jobName)
	require.NotContains(t, f.cluster.recorded(), "apply Deployment/"+f.namespace+"/"+f.webDeploymentName(t))

	run, err := f.st.GetRunByID(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "running", run.Status, "a stale failure must not fail the new attempt")

	// The watch reflects the deletion; the next pass re-creates the Job.
	f.fake.Remove(kube.ObjectRef{
		GVK:       schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"},
		Namespace: f.namespace, Name: jobName,
	})
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, 1, f.countOps("apply Job/"+f.namespace+"/"+jobName),
		"the fresh attempt applies the Job once")
}

// Kubernetes stamps a Job's creation in whole seconds while the promotion
// keeps microseconds, so a release that fails within its promotion's second
// looks created before it. Bound to its promotion (or, for a Job from before
// the label, compared at whole-second precision), the failure stays the
// promotion's own: the run fails and no later pass runs the command again.
func TestReconcileReleaseFailureWithinPromotionSecondStaysFailed(t *testing.T) {
	t.Parallel()
	for name, labeled := range map[string]bool{"labeled": true, "unlabeled": false} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
			ctx := context.Background()
			result := f.executeDeploymentManifest(t, releaseManifest)
			f.fake.SetFresh()
			jobName := f.releaseJobName(t)
			deleteJob := "delete Job/" + f.namespace + "/" + jobName

			_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
			require.NoError(t, err)

			status := observe.JobStatus{Failed: true, Reason: "BackoffLimitExceeded",
				Created: f.target(t).PromotedAt.Truncate(time.Second)}
			if labeled {
				status.Promotion = f.promotion(t)
			}
			f.fake.SetReleaseJob(f.environmentID, f.namespace, jobName, "web", status)
			for range 3 {
				_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
				require.NoError(t, err)
			}

			require.NotContains(t, f.cluster.recorded(), deleteJob,
				"a failure of this promotion must not be retried")
			require.Equal(t, 1, f.countOps("apply Job/"+f.namespace+"/"+jobName),
				"the release command runs once per promotion")
			run, err := f.st.GetRunByID(ctx, result.RunID)
			require.NoError(t, err)
			require.Equal(t, "failed", run.Status)
			require.Equal(t, "failed", f.runStepStatuses(t, result.RunID)["release:web"])
		})
	}
}

// A forced redeploy of the same revision is a new promotion: the release
// that failed under the earlier one runs again, once, bound to the new one,
// even when both promotions fall within one second.
func TestReconcileReleaseRerunsForNewPromotion(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	first := f.executeDeploymentManifest(t, releaseManifest)
	f.fake.SetFresh()
	jobName := f.releaseJobName(t)
	jobKey := "Job/" + f.namespace + "/" + jobName

	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	earlier := f.target(t).PromotedAt
	f.fake.SetReleaseJob(f.environmentID, f.namespace, jobName, "web",
		observe.JobStatus{Failed: true, Created: earlier.Truncate(time.Second), Promotion: f.promotion(t)})
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.NotContains(t, f.cluster.recorded(), "delete "+jobKey)

	revision, err := f.st.GetRevisionByID(ctx, first.RevisionID)
	require.NoError(t, err)
	forced, err := f.deploy.Execute(ctx, deploy.ExecuteInput{
		ProjectID:           f.projectID,
		EnvironmentID:       f.environmentID,
		DefinitionVersionID: revision.DefinitionVersionID,
		Resolver:            &artifactstore.Fake{Store: artifactstore.New(f.st), ProjectID: f.projectID},
		Journal:             f.journal,
		Actor:               "tester",
		Restart:             true,
	})
	require.NoError(t, err)
	require.Equal(t, first.RevisionID, forced.RevisionID)
	require.NotEqual(t, f.promotion(t), rendering.PromotionStamp(earlier))

	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Contains(t, f.cluster.recorded(), "delete "+jobKey,
		"the failure belongs to the earlier promotion")
	run, err := f.st.GetRunByID(ctx, forced.RunID)
	require.NoError(t, err)
	require.Equal(t, "running", run.Status)

	f.fake.Remove(kube.ObjectRef{
		GVK:       schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"},
		Namespace: f.namespace, Name: jobName,
	})
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, 2, f.countOps("apply "+jobKey), "the new promotion runs the release once")
	require.Equal(t, f.promotion(t), f.appliedReleaseJob(t, jobName).Labels[rendering.LabelPromotion])
}

// Cancelling a rollout falls back to the active revision, whose desired
// set no longer holds the new revision's release Job. A migration still
// running is left to finish or reach its own timeout; only a terminal Job
// is pruned.
func TestFallbackKeepsRunningReleaseJob(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()

	// Revision A releases, rolls out, and activates.
	f.executeDeploymentManifest(t, releaseManifest)
	f.fake.SetFresh()
	firstJob := f.releaseJobName(t)
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	f.fake.SetReleaseJob(f.environmentID, f.namespace, firstJob, "web",
		observe.JobStatus{Succeeded: true, Created: time.Now(), Promotion: f.promotion(t)})
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	f.markHealthy(t)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.NotNil(t, f.target(t).ActiveRevisionID)

	// Revision B's migration starts, then the rollout is cancelled.
	second := f.deployChanged(t, releaseManifest)
	secondJob := f.releaseJobName(t)
	require.NotEqual(t, firstJob, secondJob)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Contains(t, f.cluster.recorded(), "apply Job/"+f.namespace+"/"+secondJob)
	secondPromotion := f.promotion(t)
	f.fake.SetReleaseJob(f.environmentID, f.namespace, secondJob, "web",
		observe.JobStatus{Created: time.Now(), Promotion: secondPromotion})

	outcome, err := f.deploy.CancelRollout(ctx, deploy.CancelRolloutInput{
		RunID: second.RunID, EnvironmentID: f.environmentID,
		RevisionID: &second.RevisionID, Journal: f.journal,
	})
	require.NoError(t, err)
	require.True(t, outcome.Fallback)

	deleteSecond := "delete Job/" + f.namespace + "/" + secondJob
	for range 2 {
		_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
		require.NoError(t, err)
	}
	require.NotContains(t, f.cluster.recorded(), deleteSecond,
		"a running migration must not be pruned mid-command")

	// The command ends (here past its deadline); the next pass reclaims it.
	f.fake.SetReleaseJob(f.environmentID, f.namespace, secondJob, "web",
		observe.JobStatus{Failed: true, Reason: "DeadlineExceeded", Created: time.Now(), Promotion: secondPromotion})
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, 1, f.countOps(deleteSecond))

	run, err := f.st.GetRunByID(ctx, second.RunID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", run.Status, "a late failure does not rewrite the run's outcome")
}
