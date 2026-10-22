package reconcile

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/artifactstore"
	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/deploy"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/store"
)

// heldResolver holds a deployment inside its artifact resolution, between
// beginning its run and promoting it, until released.
type heldResolver struct {
	inner   deploy.ArtifactResolver
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *heldResolver) Resolve(ctx context.Context, application string, source compiler.ApplicationSource) (artifactstore.Resolved, error) {
	r.once.Do(func() { close(r.reached) })
	<-r.release
	return r.inner.Resolve(ctx, application, source)
}

// deployActive rolls the fixture manifest out and activates it.
func (f *kernelFixture) deployActive(t *testing.T) *deploy.ExecuteResult {
	t.Helper()
	ctx := context.Background()
	result := f.executeDeployment(t)
	f.fake.SetFresh()
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	f.markHealthy(t)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, result.RevisionID, *f.target(t).ActiveRevisionID)
	return result
}

// definitionVersion submits manifest as the project's draft and returns
// its definition version.
func (f *kernelFixture) definitionVersion(t *testing.T, manifest string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	projects := project.New(f.st)
	draft, err := projects.GetDraft(ctx, f.projectID)
	require.NoError(t, err)
	submitted, err := projects.SubmitDraft(ctx, f.projectID, project.DraftSubmission{
		Source: []byte(manifest), Format: "yaml", ExpectedVersion: draft.Version,
	})
	require.NoError(t, err)
	row, err := f.st.GetDefinitionVersionByHash(ctx, store.GetDefinitionVersionByHashParams{
		ProjectID: f.projectID, DefinitionHash: submitted.Hash,
	})
	require.NoError(t, err)
	return row.ID
}

// A pass between a deployment beginning its run and promoting it neither
// journals into the run nor concludes it, even on an environment converged
// on the previous target, where activation would close a run as up to
// date. The promotion hands the run off, and the next pass adopts and
// finishes it. A forced redeploy of the active revision takes the same
// path: its target never even moves.
func TestPassBeforeHandOffLeavesRunRunning(t *testing.T) {
	t.Parallel()
	for name, change := range map[string]struct {
		manifest string
		restart  bool
	}{
		"deploy":          {manifest: strings.Replace(kernelManifest, ":1.0.0", ":2.0.0", 1)},
		"forced redeploy": {manifest: kernelManifest, restart: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
			ctx := context.Background()
			first := f.deployActive(t)

			resolver := &heldResolver{
				inner:   &artifactstore.Fake{Store: artifactstore.New(f.st), ProjectID: f.projectID},
				reached: make(chan struct{}), release: make(chan struct{}),
			}
			type outcome struct {
				result *deploy.ExecuteResult
				err    error
			}
			done := make(chan outcome, 1)
			definitionVersionID := f.definitionVersion(t, change.manifest)
			go func() {
				result, err := f.deploy.Execute(ctx, deploy.ExecuteInput{
					ProjectID: f.projectID, EnvironmentID: f.environmentID,
					DefinitionVersionID: definitionVersionID, Resolver: resolver,
					Journal: f.journal, Actor: "tester", Restart: change.restart,
				})
				done <- outcome{result, err}
			}()
			<-resolver.reached

			opened, err := f.st.GetRunningRunByEnvironment(ctx, &f.environmentID)
			require.NoError(t, err)
			require.Nil(t, opened.RolloutRevisionID, "nothing is handed off before promotion")
			_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
			require.NoError(t, err)
			run, err := f.st.GetRunByID(ctx, opened.ID)
			require.NoError(t, err)
			require.Equal(t, "running", run.Status,
				"a pass that read the previous target must not conclude the new run")
			keys := f.runStepStatuses(t, opened.ID)
			require.NotContains(t, keys, "rollout")
			require.NotContains(t, keys, "activate")

			close(resolver.release)
			promoted := <-done
			require.NoError(t, promoted.err)
			require.Equal(t, opened.ID, promoted.result.RunID)
			if change.restart {
				require.Equal(t, first.RevisionID, promoted.result.RevisionID)
			}
			run, err = f.st.GetRunByID(ctx, opened.ID)
			require.NoError(t, err)
			require.Equal(t, "running", run.Status)
			require.Equal(t, &promoted.result.RevisionID, run.RolloutRevisionID,
				"the promotion hands the run off for the revision it moved the target to")

			_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
			require.NoError(t, err)
			f.markHealthy(t)
			_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
			require.NoError(t, err)
			run, err = f.st.GetRunByID(ctx, opened.ID)
			require.NoError(t, err)
			require.Equal(t, "succeeded", run.Status)
			keys = f.runStepStatuses(t, opened.ID)
			require.Equal(t, "succeeded", keys["promote"])
			require.Equal(t, "succeeded", keys["rollout"])
			require.Equal(t, promoted.result.RevisionID, *f.target(t).ActiveRevisionID)
		})
	}
}

// A restart hands its run off in the transaction that stamps it, for the
// revision it restarts, and the kernel adopts the run from the next pass.
func TestRestartHandsOffRun(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	active := f.deployActive(t)

	result, err := f.deploy.Restart(ctx, deploy.RestartInput{
		EnvironmentID: f.environmentID, Actor: "tester", Journal: f.journal,
	})
	require.NoError(t, err)
	run, err := f.st.GetRunByID(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, &active.RevisionID, run.RolloutRevisionID)

	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, "running", f.runStepStatuses(t, result.RunID)["rollout"],
		"the pass adopted the restart run")
	f.markHealthy(t)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	run, err = f.st.GetRunByID(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", run.Status)
}

// A run handed off for a revision the target has since left is over: the
// kernel concludes it instead of journaling the current target's rollout
// into it, and the pass goes on as if nothing ran.
func TestRunBoundToLeftRevisionConcluded(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	f.deployActive(t)

	stale, err := f.journal.BeginRun(ctx, journal.RunInput{
		Kind: "deployment", ProjectID: f.projectID, EnvironmentID: f.environmentID, Actor: "tester",
	})
	require.NoError(t, err)
	_, err = f.st.HandOffRun(ctx, store.HandOffRunParams{ID: stale.ID, RolloutRevisionID: uuid.New()})
	require.NoError(t, err)

	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	run, err := f.st.GetRunByID(ctx, stale.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", run.Status)
	require.NotNil(t, run.Failure)
	require.Contains(t, *run.Failure, "moved off")
	require.Empty(t, f.runStepStatuses(t, stale.ID), "nothing of the current target is journaled into it")
}
