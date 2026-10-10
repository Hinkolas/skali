package deploy

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/artifactstore"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/store"
)

// promoteRunning executes a deployment of manifest whose run stays
// running for the kernel, as with a kernel wired.
func (f *fixture) promoteRunning(t *testing.T, jsvc *journal.Service, manifest, secret string) *ExecuteResult {
	t.Helper()
	ctx := context.Background()
	f.deploy.SetEnqueuer(&recordingEnqueuer{})
	definitionVersion, _, err := f.projects.SubmitCandidate(ctx, f.projectID, []byte(manifest), "yaml")
	require.NoError(t, err)
	candidate := f.stage(t, map[string]string{"APP_DOMAIN": "demo.example.com", "SESSION_SECRET": secret})
	result, err := f.execute(t, jsvc, definitionVersion, candidate.ID,
		&artifactstore.Fake{Store: f.artifacts, ProjectID: f.projectID})
	require.NoError(t, err)
	return result
}

func (f *fixture) activateTarget(t *testing.T) {
	t.Helper()
	target, err := f.deploy.Target(context.Background(), f.environmentID)
	require.NoError(t, err)
	rows, err := f.st.SetEnvironmentActiveRevision(context.Background(), store.SetEnvironmentActiveRevisionParams{
		EnvironmentID: f.environmentID, ActiveRevisionID: target.TargetRevisionID,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)
}

func (f *fixture) runStatus(t *testing.T, jsvc *journal.Service, id uuid.UUID) string {
	t.Helper()
	run, err := jsvc.Run(context.Background(), id)
	require.NoError(t, err)
	return run.Status
}

// A cancel that cannot take the environment lock changes nothing, so it
// can be retried: the run stays running and the target stays put. Once it
// gets the lock, the target falls back and the run is cancelled together.
func TestCancelRolloutChangesNothingUntilItHoldsTheLock(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.deploy.requestWait = 200 * time.Millisecond
	ctx := context.Background()
	jsvc := journal.NewService(f.st, "executor-1")
	first := f.promoteRunning(t, jsvc, testManifest, "cancel-first-value")
	f.activateTarget(t)
	require.NoError(t, jsvc.FinishRun(ctx, first.RunID, journal.RunSucceeded))
	second := f.promoteRunning(t, jsvc, changedManifest, "cancel-second-value")
	input := CancelRolloutInput{
		RunID: second.RunID, EnvironmentID: f.environmentID, RevisionID: &second.RevisionID, Journal: jsvc,
	}

	unlock, err := f.st.LockEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	_, err = f.deploy.CancelRollout(ctx, input)
	require.ErrorIs(t, err, ErrEnvironmentBusy)
	require.Equal(t, string(journal.RunRunning), f.runStatus(t, jsvc, second.RunID))
	target, err := f.deploy.Target(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, second.RevisionID, *target.TargetRevisionID)
	unlock()

	outcome, err := f.deploy.CancelRollout(ctx, input)
	require.NoError(t, err)
	require.Equal(t, CancelOutcome{Fallback: true}, outcome)
	require.Equal(t, string(journal.RunCancelled), f.runStatus(t, jsvc, second.RunID))
	target, err = f.deploy.Target(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, first.RevisionID, *target.TargetRevisionID)

	_, err = f.deploy.CancelRollout(ctx, input)
	var concluded *RunConcludedError
	require.ErrorAs(t, err, &concluded)
	require.Equal(t, "cancelled", concluded.Status)
}

// A first deployment has nothing to fall back to: the cancel concludes the
// run, and the revision stays the target.
func TestCancelRolloutOfAFirstDeploymentKeepsItsTarget(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	jsvc := journal.NewService(f.st, "executor-1")
	first := f.promoteRunning(t, jsvc, testManifest, "cancel-only-value")

	outcome, err := f.deploy.CancelRollout(ctx, CancelRolloutInput{
		RunID: first.RunID, EnvironmentID: f.environmentID, RevisionID: &first.RevisionID, Journal: jsvc,
	})
	require.NoError(t, err)
	require.Equal(t, CancelOutcome{Continues: true}, outcome)
	require.Equal(t, string(journal.RunCancelled), f.runStatus(t, jsvc, first.RunID))
	target, err := f.deploy.Target(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, first.RevisionID, *target.TargetRevisionID)
}

// A rollout whose revision already activated is no longer cancelled; the
// run is left for the pass that concludes it.
func TestCancelRolloutRefusesAnActivatedRevision(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	jsvc := journal.NewService(f.st, "executor-1")
	first := f.promoteRunning(t, jsvc, testManifest, "cancel-active-value")
	f.activateTarget(t)

	_, err := f.deploy.CancelRollout(ctx, CancelRolloutInput{
		RunID: first.RunID, EnvironmentID: f.environmentID, RevisionID: &first.RevisionID, Journal: jsvc,
	})
	require.ErrorIs(t, err, ErrRevisionActive)
	require.Equal(t, string(journal.RunRunning), f.runStatus(t, jsvc, first.RunID))
}
