package reconcile

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/artifactstore"
	"github.com/Hinkolas/skali/internal/deploy"
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
