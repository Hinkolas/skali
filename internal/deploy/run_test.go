package deploy

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/artifactstore"
	"github.com/Hinkolas/skali/internal/journal"
)

func (f *fixture) execute(t *testing.T, jsvc *journal.Service, definitionVersion, candidateID uuid.UUID, resolver ArtifactResolver) (*ExecuteResult, error) {
	t.Helper()
	return f.deploy.Execute(context.Background(), ExecuteInput{
		ProjectID:           f.projectID,
		EnvironmentID:       f.environmentID,
		DefinitionVersionID: definitionVersion,
		CandidateID:         candidateID,
		Resolver:            resolver,
		Journal:             jsvc,
		Actor:               "tester",
	})
}

func TestExecuteEndToEnd(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	jsvc := journal.NewService(f.st, "executor-1")

	definitionVersion := f.submit(t, testManifest, 0)
	candidate := f.stage(t,
		map[string]string{"APP_DOMAIN": "demo.example.com"},
		map[string]string{"SESSION_SECRET": "execute-plant-value"})

	result, err := f.execute(t, jsvc, definitionVersion, candidate.ID,
		&artifactstore.Fake{Store: f.artifacts, ProjectID: f.projectID})
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, result.RevisionID)

	// The run tree explains the deployment: both steps succeeded.
	tree, err := jsvc.RunTree(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", tree.Run.Status)
	require.Len(t, tree.Steps, 2)
	require.Equal(t, "prepare", tree.Steps[0].Step.Key)
	require.Equal(t, "succeeded", tree.Steps[0].Step.Status)
	require.Equal(t, "promote", tree.Steps[1].Step.Key)
	require.Equal(t, "succeeded", tree.Steps[1].Step.Status)

	// The target moved and values are current.
	target, err := f.deploy.Target(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, result.RevisionID, *target.TargetRevisionID)

	// Runs explain but never drive: deleting the whole journal changes
	// neither the target nor any authoritative row.
	_, err = f.st.Pool.Exec(ctx, "DELETE FROM runs")
	require.NoError(t, err)
	after, err := f.deploy.Target(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, target.TargetRevisionID, after.TargetRevisionID)
	require.Equal(t, target.UpdatedAt, after.UpdatedAt)
	rows, err := f.deploy.ListRevisions(ctx, f.environmentID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

// Exit criterion (full): a failed candidate preparation does not promote
// draft or value versions and does not move the environment target, and the
// run records the failure with redacted diagnostics.
func TestExecuteFailureDoesNotPromote(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	jsvc := journal.NewService(f.st, "executor-1")

	definitionVersion := f.submit(t, testManifest, 0)
	first := f.stage(t,
		map[string]string{"APP_DOMAIN": "demo.example.com"},
		map[string]string{"SESSION_SECRET": "stable-plant-value"})
	_, err := f.execute(t, jsvc, definitionVersion, first.ID,
		&artifactstore.Fake{Store: f.artifacts, ProjectID: f.projectID})
	require.NoError(t, err)

	draftBefore, err := f.projects.GetDraft(ctx, f.projectID)
	require.NoError(t, err)
	targetBefore, err := f.deploy.Target(ctx, f.environmentID)
	require.NoError(t, err)

	changedVersion, _, err := f.projects.SubmitCandidate(ctx, f.projectID, []byte(changedManifest), "yaml")
	require.NoError(t, err)
	second := f.stage(t,
		map[string]string{"APP_DOMAIN": "changed.example.com"},
		map[string]string{"SESSION_SECRET": "failed-plant-value"})

	boom := errors.New("build failed: secret failed-plant-value leaked into output")
	result, err := f.execute(t, jsvc, changedVersion, second.ID, &artifactstore.Fake{
		Store: f.artifacts, ProjectID: f.projectID,
		FailFor: map[string]error{"web": boom},
	})
	require.ErrorIs(t, err, boom)

	// The run is failed with terminal steps.
	tree, err := jsvc.RunTree(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "failed", tree.Run.Status)
	require.Equal(t, "failed", tree.Steps[0].Step.Status)

	// The failure diagnostic is recorded, with the staged secret redacted.
	var combined string
	require.NoError(t, f.st.Pool.QueryRow(ctx,
		"SELECT string_agg(message, ' ') FROM run_logs").Scan(&combined))
	require.Contains(t, combined, "preparation failed")
	require.NotContains(t, combined, "failed-plant-value")
	require.Contains(t, combined, "[redacted:SESSION_SECRET]")

	// Nothing promoted, nothing moved, staged rows discarded.
	draftAfter, err := f.projects.GetDraft(ctx, f.projectID)
	require.NoError(t, err)
	require.Equal(t, draftBefore.Version, draftAfter.Version)
	targetAfter, err := f.deploy.Target(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, targetBefore.TargetRevisionID, targetAfter.TargetRevisionID)
	require.Equal(t, targetBefore.UpdatedAt, targetAfter.UpdatedAt)
	var staged int
	require.NoError(t, f.st.Pool.QueryRow(ctx,
		"SELECT (SELECT count(*) FROM environment_values WHERE state = 'staged') + (SELECT count(*) FROM environment_secrets WHERE state = 'staged')").Scan(&staged))
	require.Zero(t, staged)
}

// Exit criterion: secrets cannot appear in revision fixtures or run logs.
// After a full deployment with a planted secret, a raw scan of every
// persisted surface finds the plaintext only in the encrypted secret store.
func TestDeploymentPersistsNoSecretPlaintext(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	jsvc := journal.NewService(f.st, "executor-1")

	const planted = "s3cr3t-plant-value-8829"
	definitionVersion := f.submit(t, testManifest, 0)
	candidate := f.stage(t,
		map[string]string{"APP_DOMAIN": "demo.example.com"},
		map[string]string{"SESSION_SECRET": planted})
	_, err := f.execute(t, jsvc, definitionVersion, candidate.ID,
		&artifactstore.Fake{Store: f.artifacts, ProjectID: f.projectID})
	require.NoError(t, err)

	for _, query := range []string{
		"SELECT COALESCE(string_agg(document::text, ' '), '') FROM revisions",
		"SELECT COALESCE(string_agg(message, ' '), '') FROM run_logs",
		"SELECT COALESCE(string_agg(fields::text, ' '), '') FROM run_logs",
		"SELECT COALESCE(string_agg(value, ' '), '') FROM environment_values",
		"SELECT COALESCE(string_agg(definition::text, ' '), '') FROM definition_versions",
		"SELECT COALESCE(string_agg(provenance::text, ' '), '') FROM artifacts",
	} {
		var text string
		require.NoError(t, f.st.Pool.QueryRow(ctx, query).Scan(&text))
		require.NotContains(t, text, planted, "query: %s", query)
	}
	// The ciphertext column holds it, encrypted: the bytes differ.
	var ciphertext []byte
	require.NoError(t, f.st.Pool.QueryRow(ctx,
		"SELECT ciphertext FROM environment_secrets WHERE name = 'SESSION_SECRET'").Scan(&ciphertext))
	require.NotContains(t, string(ciphertext), planted)
}

// Exit criterion: restart tests preserve target and resumable run identity.
func TestRestartPreservesTargetAndRunIdentity(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	jsvc := journal.NewService(f.st, "executor-1")

	// Deploy to establish a target.
	definitionVersion := f.submit(t, testManifest, 0)
	candidate := f.stage(t,
		map[string]string{"APP_DOMAIN": "demo.example.com"},
		map[string]string{"SESSION_SECRET": "restart-plant-value"})
	deployed, err := f.execute(t, jsvc, definitionVersion, candidate.ID,
		&artifactstore.Fake{Store: f.artifacts, ProjectID: f.projectID})
	require.NoError(t, err)
	targetBefore, err := f.deploy.Target(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, deployed.RevisionID, *targetBefore.TargetRevisionID)

	// A second run is mid-flight when the daemon dies.
	run, err := jsvc.CreateRun(ctx, journal.RunInput{
		Kind: "deployment", ProjectID: f.projectID, EnvironmentID: f.environmentID, Actor: "tester",
	})
	require.NoError(t, err)
	require.NoError(t, jsvc.StartRun(ctx, run.ID))
	step, err := jsvc.EnsureStep(ctx, run.ID, nil, "apply:web", "Apply web")
	require.NoError(t, err)
	require.NoError(t, jsvc.SetStepStatus(ctx, step.ID, journal.StepRunning))
	attempt, err := jsvc.StartAttempt(ctx, step.ID)
	require.NoError(t, err)

	// Simulated restart: fresh service instances over the same pool with a
	// new executor identity. Boot recovery runs; targets are never touched.
	restarted := journal.NewService(f.st, "executor-2")
	failed, err := restarted.RecoverOnBoot(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), failed)

	targetAfter, err := f.deploy.Target(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, targetBefore.TargetRevisionID, targetAfter.TargetRevisionID)
	require.Equal(t, targetBefore.ActiveRevisionID, targetAfter.ActiveRevisionID)
	require.Equal(t, targetBefore.UpdatedAt, targetAfter.UpdatedAt)

	// The run keeps its identity and status; the orphaned attempt failed
	// with a restart diagnostic; the deterministic key reattaches to the
	// same step and the next attempt continues the same run.
	tree, err := restarted.RunTree(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, "running", tree.Run.Status)
	orphaned, err := f.st.GetAttemptByID(ctx, attempt.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", orphaned.Status)
	logs, err := restarted.StepLogs(ctx, step.ID, journal.Cursor{}, 10)
	require.NoError(t, err)
	require.NotEmpty(t, logs)
	require.Contains(t, logs[len(logs)-1].Message, "daemon restarted")

	same, err := restarted.EnsureStep(ctx, run.ID, nil, "apply:web", "Apply web")
	require.NoError(t, err)
	require.Equal(t, step.ID, same.ID)
	next, err := restarted.StartAttempt(ctx, step.ID)
	require.NoError(t, err)
	require.Equal(t, int64(2), next.Number)
	require.NoError(t, restarted.FinishAttempt(ctx, next.ID, journal.AttemptSucceeded))
	require.NoError(t, restarted.SetStepStatus(ctx, step.ID, journal.StepSucceeded))
	require.NoError(t, restarted.FinishRun(ctx, run.ID, journal.RunSucceeded))
}

func TestExecuteRefusesConcurrentDeployment(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	jsvc := journal.NewService(f.st, "executor-1")

	// Hold an unrelated run in running state for this environment.
	blocker, err := jsvc.CreateRun(ctx, journal.RunInput{
		Kind: "deployment", EnvironmentID: f.environmentID,
	})
	require.NoError(t, err)
	require.NoError(t, jsvc.StartRun(ctx, blocker.ID))

	definitionVersion := f.submit(t, testManifest, 0)
	candidate := f.stage(t,
		map[string]string{"APP_DOMAIN": "demo.example.com"},
		map[string]string{"SESSION_SECRET": "concurrent-plant-value"})
	_, err = f.execute(t, jsvc, definitionVersion, candidate.ID,
		&artifactstore.Fake{Store: f.artifacts, ProjectID: f.projectID})
	require.ErrorIs(t, err, ErrDeploymentInFlight)
}
