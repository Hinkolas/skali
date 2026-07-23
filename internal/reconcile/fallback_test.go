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
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/store"
)

// deployChanged promotes a second revision (different image tag) on top of
// whatever the fixture deployed before.
func (f *kernelFixture) deployChanged(t *testing.T) *deploy.ExecuteResult {
	t.Helper()
	ctx := context.Background()
	projects := project.New(f.st)
	draft, err := projects.GetDraft(ctx, f.projectID)
	require.NoError(t, err)
	submitted, err := projects.SubmitDraft(ctx, f.projectID, project.DraftSubmission{
		Source:          []byte(strings.Replace(kernelManifest, ":1.0.0", ":2.0.0", 1)),
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
	f.fake.SetWorkload(f.environmentID, f.namespace, "demo-web", "web", "",
		module.WorkloadStatus{Desired: 1, Ready: 0})
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
	second := f.deployChanged(t)
	require.NotEqual(t, first.RevisionID, second.RevisionID)
	f.kernel.cfg.RolloutDeadline = time.Nanosecond
	f.fake.SetWorkload(f.environmentID, f.namespace, "demo-web", "web", "",
		module.WorkloadStatus{Desired: 1, Ready: 0})

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
