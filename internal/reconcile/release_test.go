package reconcile

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/kube"
	rendering "github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/observe"
)

const releaseManifest = `version: "1"
name: demo
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
	require.NotContains(t, ops, "apply Deployment/"+f.namespace+"/app-demo-web-714832ea87e5bc991f3f11667354c6c3",
		"the workload must not roll before the release command completes")

	// Still running: another pass neither re-creates the Job nor applies.
	f.fake.SetReleaseJob(f.environmentID, f.namespace, jobName, "web",
		observe.JobStatus{Created: time.Now()})
	requeue, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, requeueHealthCheck, requeue)
	require.NotContains(t, f.cluster.recorded(), "apply Deployment/"+f.namespace+"/app-demo-web-714832ea87e5bc991f3f11667354c6c3")

	// Success unblocks the workload; health then activates the revision.
	f.fake.SetReleaseJob(f.environmentID, f.namespace, jobName, "web",
		observe.JobStatus{Succeeded: true, Created: time.Now()})
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Contains(t, f.cluster.recorded(), "apply Deployment/"+f.namespace+"/app-demo-web-714832ea87e5bc991f3f11667354c6c3")

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
			Created: time.Now().Add(time.Minute)})
	f.drainQueue(t)
	requeue, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Zero(t, requeue)
	require.Zero(t, f.kernel.queue.Len(),
		"a first deployment has nothing to fall back to and must not requeue")

	run, err := f.st.GetRunByID(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "failed", run.Status)
	require.NotContains(t, f.cluster.recorded(), "apply Deployment/"+f.namespace+"/app-demo-web-714832ea87e5bc991f3f11667354c6c3")

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
	require.NotContains(t, f.cluster.recorded(), "apply Deployment/"+f.namespace+"/app-demo-web-714832ea87e5bc991f3f11667354c6c3")

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
	applies := 0
	for _, op := range f.cluster.recorded() {
		if op == "apply Job/"+f.namespace+"/"+jobName {
			applies++
		}
	}
	require.Equal(t, 1, applies, "the fresh attempt applies the Job once")
}
