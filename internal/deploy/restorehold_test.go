package deploy

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/artifactstore"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/store"
)

// hold stops the environment for a restore of revision the way the
// restore's stop does, under a running restore run, and returns the
// restore's id and its run.
func (f *fixture) hold(t *testing.T, jsvc *journal.Service, revision uuid.UUID) (uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	run, err := jsvc.BeginRun(ctx, journal.RunInput{
		Kind: "restore", ProjectID: f.projectID, EnvironmentID: f.environmentID, Actor: "test",
	})
	require.NoError(t, err)
	row, err := f.st.CreateBackup(ctx, store.CreateBackupParams{
		ID: uuid.New(), Kind: "restore", EnvironmentID: f.environmentID, ProjectName: "demo",
		EnvironmentName: "production", RevisionID: &revision, RunID: &run.ID, Trigger: "manual", Strategy: "complete",
	})
	require.NoError(t, err)
	marked, err := f.st.MarkEnvironmentRestoring(ctx, store.MarkEnvironmentRestoringParams{
		EnvironmentID: f.environmentID, RestoreID: row.ID, Components: []string{"db:data"},
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, marked)
	return row.ID, run.ID
}

// An environment an incomplete restore holds is never brought back by a
// deployment: opening one, promoting a prepared one, and rolling back are
// all refused with the restore that holds it, and nothing moves. Only the
// holding restore resumes it, and only while its run runs: a teardown
// cancels the run before it takes the environment down, and a restore
// finishing afterwards must not undo that. Resuming lifts the hold, and
// deployments work again from there.
func TestRestoreHoldRefusesDeployments(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	jsvc := journal.NewService(f.st, "test")
	resolver := &artifactstore.Fake{Store: f.artifacts, ProjectID: f.projectID}
	definitionVersion := f.submit(t, testManifest, 0)
	candidate := f.stage(t, map[string]string{"APP_DOMAIN": "demo.example.com", "SESSION_SECRET": "hold-plant-value"})
	first, err := f.deploy.Prepare(ctx, PrepareInput{
		EnvironmentID: f.environmentID, DefinitionVersionID: definitionVersion,
		CandidateID: candidate.ID, Resolver: resolver,
	})
	require.NoError(t, err)
	require.NoError(t, f.deploy.Promote(ctx, first))
	changedVersion, _, err := f.projects.SubmitCandidate(ctx, f.projectID, []byte(changedManifest), "yaml")
	require.NoError(t, err)
	second, err := f.deploy.Prepare(ctx, PrepareInput{
		EnvironmentID: f.environmentID, DefinitionVersionID: changedVersion, Resolver: resolver,
	})
	require.NoError(t, err)

	restoreID, restoreRun := f.hold(t, jsvc, first.RevisionID)
	refused := func(err error) {
		t.Helper()
		var held *RestoreIncompleteError
		require.ErrorAs(t, err, &held)
		require.ErrorIs(t, err, ErrRestoreIncomplete)
		require.Equal(t, restoreID, held.RestoreID)
		target, err := f.deploy.Target(ctx, f.environmentID)
		require.NoError(t, err)
		require.Equal(t, EnvironmentStateDown, target.State)
		require.Nil(t, target.TargetRevisionID)
	}
	refused(f.deploy.Promote(ctx, second))
	_, err = f.deploy.Rollback(ctx, RollbackInput{
		EnvironmentID: f.environmentID, RevisionID: first.RevisionID, Actor: "test", Journal: jsvc,
	})
	refused(err)
	_, err = f.deploy.Open(ctx, OpenInput{
		PlanInput: PlanInput{EnvironmentID: f.environmentID, DefinitionVersionID: changedVersion},
		Actor:     "test", Capabilities: []string{"application"}, RegistryConfigured: true, Journal: jsvc,
	})
	refused(err)
	running, err := f.st.GetRunningRunByEnvironment(ctx, &f.environmentID)
	require.NoError(t, err)
	require.Equal(t, restoreRun, running.ID, "a refused deployment leaves no run behind")

	require.ErrorIs(t, f.deploy.ResumeRevision(ctx, f.environmentID, first.RevisionID, uuid.New()), ErrRestoreHoldLost,
		"only the restore that holds the environment resumes it")
	require.NoError(t, jsvc.FinishRun(ctx, restoreRun, journal.RunCancelled))
	require.ErrorIs(t, f.deploy.ResumeRevision(ctx, f.environmentID, first.RevisionID, restoreID), ErrRestoreHoldLost,
		"a cancelled restore does not resume")
	refused(f.deploy.Promote(ctx, second))

	restoreID, _ = f.hold(t, jsvc, first.RevisionID)
	require.NoError(t, f.deploy.ResumeRevision(ctx, f.environmentID, first.RevisionID, restoreID))
	target, err := f.deploy.Target(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, EnvironmentStateActive, target.State)
	require.Equal(t, first.RevisionID, *target.TargetRevisionID)
	require.Nil(t, target.IncompleteRestoreID)
	require.Empty(t, target.IncompleteRestoreComponents)

	require.NoError(t, f.deploy.Promote(ctx, second))
}
