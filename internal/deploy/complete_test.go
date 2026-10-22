package deploy

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/store"
)

const testDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// openForComplete opens a deployment against the image-sourced test
// manifest in the given environment and verifies every artifact it asked
// for, leaving the window ready for Complete.
func (f *fixture) openForComplete(t *testing.T, jsvc *journal.Service, environmentID, definitionVersion uuid.UUID, secret string) *store.Deployment {
	t.Helper()
	ctx := context.Background()
	candidate, err := f.values.Stage(ctx, environmentID,
		map[string]string{"APP_DOMAIN": "demo.example.com", "SESSION_SECRET": secret})
	require.NoError(t, err)
	opened, err := f.deploy.Open(ctx, OpenInput{
		PlanInput: PlanInput{
			EnvironmentID:       environmentID,
			DefinitionVersionID: definitionVersion,
			CandidateID:         candidate.ID,
		},
		Actor:              "tester",
		Capabilities:       []string{"application"},
		RegistryConfigured: true,
		Journal:            jsvc,
	})
	require.NoError(t, err)
	require.NotNil(t, opened.Deployment)
	for _, action := range opened.Actions {
		if action.Action == "reuse" {
			continue // already verified by an earlier window
		}
		require.NoError(t, f.artifacts.Verify(ctx, action.ArtifactID, "registry.test/demo/web", testDigest, nil))
	}
	return opened.Deployment
}

// awaitDeploymentStatus polls until the deployment leaves preparing.
func (f *fixture) awaitDeploymentStatus(t *testing.T, id uuid.UUID) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		row, err := f.deploy.GetDeployment(context.Background(), id)
		require.NoError(t, err)
		if row.Status != string(DeploymentPreparing) {
			return row.Status
		}
		require.True(t, time.Now().Before(deadline), "deployment still preparing")
		time.Sleep(10 * time.Millisecond)
	}
}

// The production incident: promotion waits on the environment lock longer
// than the client is willing to, the client's context dies, and the
// cleanup must still close the run and the deployment so the environment
// is free again.
func TestCompleteSurvivesClientDisconnect(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	jsvc := journal.NewService(f.st, "executor-1")
	definitionVersion := f.submit(t, testManifest, 0)
	deployment := f.openForComplete(t, jsvc, f.environmentID, definitionVersion, "disconnect-plant-value")

	// Another party (the reconcile kernel, in production) holds the lock.
	unlock, err := f.st.LockEnvironment(ctx, f.environmentID)
	require.NoError(t, err)

	c, err := f.deploy.beginComplete(ctx, deployment.ID, jsvc)
	require.NoError(t, err)
	defer f.deploy.release(deployment.ID)

	requestCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	_, err = f.deploy.finishComplete(requestCtx, c)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	unlock()

	tree, err := jsvc.RunTree(ctx, c.runID)
	require.NoError(t, err)
	require.Equal(t, "failed", tree.Run.Status, "the run must not stay running")
	for _, step := range tree.Steps {
		require.NotEqual(t, "running", step.Step.Status, "step %s left running", step.Step.Key)
	}
	row, err := f.deploy.GetDeployment(ctx, deployment.ID)
	require.NoError(t, err)
	require.Equal(t, string(DeploymentFailed), row.Status)
	target, err := f.deploy.Target(ctx, f.environmentID)
	require.NoError(t, err)
	require.Nil(t, target.TargetRevisionID, "nothing promoted")
	var staged int
	require.NoError(t, f.st.Pool.QueryRow(ctx,
		"SELECT count(*) FROM environment_secrets WHERE state = 'staged'").Scan(&staged))
	require.Zero(t, staged, "the staged candidate was discarded")

	// The environment is free: the next deployment opens.
	f.deploy.release(deployment.ID)
	next := f.openForComplete(t, jsvc, f.environmentID, definitionVersion, "retry-plant-value")
	require.NotEqual(t, deployment.ID, next.ID)
}

// A promotion that waited out a pass holding the lock fails the deployment
// as busy instead of waiting on, and moves nothing.
func TestCompleteFailsBusyWhenLockStaysHeld(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.deploy.promotionWait = 200 * time.Millisecond
	ctx := context.Background()
	jsvc := journal.NewService(f.st, "executor-1")
	definitionVersion := f.submit(t, testManifest, 0)
	deployment := f.openForComplete(t, jsvc, f.environmentID, definitionVersion, "busy-plant-value")

	unlock, err := f.st.LockEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	defer unlock()
	c, err := f.deploy.beginComplete(ctx, deployment.ID, jsvc)
	require.NoError(t, err)
	defer f.deploy.release(deployment.ID)
	_, err = f.deploy.finishComplete(ctx, c)
	require.ErrorIs(t, err, ErrEnvironmentBusy)

	tree, err := jsvc.RunTree(ctx, c.runID)
	require.NoError(t, err)
	require.Equal(t, "failed", tree.Run.Status)
	require.NotNil(t, tree.Run.Failure)
	require.Contains(t, *tree.Run.Failure, "environment busy")
	row, err := f.deploy.GetDeployment(ctx, deployment.ID)
	require.NoError(t, err)
	require.Equal(t, string(DeploymentFailed), row.Status)
	target, err := f.deploy.Target(ctx, f.environmentID)
	require.NoError(t, err)
	require.Nil(t, target.TargetRevisionID, "nothing promoted")
}

// The request context ending is not a failure of the deployment: the
// promotion runs on the service lifetime and lands.
func TestCompletePromotesOnLifetimeContextAfterRequestCancelled(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	jsvc := journal.NewService(f.st, "executor-1")
	definitionVersion := f.submit(t, testManifest, 0)
	deployment := f.openForComplete(t, jsvc, f.environmentID, definitionVersion, "lifetime-plant-value")

	requestCtx, cancel := context.WithCancel(ctx)
	c, err := f.deploy.beginComplete(requestCtx, deployment.ID, jsvc)
	require.NoError(t, err)
	defer f.deploy.release(deployment.ID)
	cancel()

	result, err := f.deploy.finishComplete(ctx, c)
	require.NoError(t, err)
	row, err := f.deploy.GetDeployment(ctx, deployment.ID)
	require.NoError(t, err)
	require.Equal(t, string(DeploymentPromoted), row.Status)
	require.Equal(t, result.RevisionID, *row.RevisionID)
	target, err := f.deploy.Target(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, result.RevisionID, *target.TargetRevisionID)
	tree, err := jsvc.RunTree(ctx, c.runID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", tree.Run.Status, "no kernel wired: promotion concludes the run")
}

// A client that retries complete while the first completion is still
// promoting must not start a second promotion.
func TestCompleteRefusesConcurrentCompletion(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	jsvc := journal.NewService(f.st, "executor-1")
	definitionVersion := f.submit(t, testManifest, 0)
	deployment := f.openForComplete(t, jsvc, f.environmentID, definitionVersion, "concurrent-plant-value")

	unlock, err := f.st.LockEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	runID, err := f.deploy.Complete(ctx, deployment.ID, jsvc)
	require.NoError(t, err)
	require.Equal(t, *deployment.RunID, runID)

	_, err = f.deploy.Complete(ctx, deployment.ID, jsvc)
	require.ErrorIs(t, err, ErrDeploymentCompleting)
	row, err := f.deploy.GetDeployment(ctx, deployment.ID)
	require.NoError(t, err)
	require.Equal(t, string(DeploymentPreparing), row.Status, "still waiting on the lock")

	unlock()
	require.Equal(t, string(DeploymentPromoted), f.awaitDeploymentStatus(t, deployment.ID))
	_, err = f.deploy.Complete(ctx, deployment.ID, jsvc)
	require.ErrorIs(t, err, ErrInvalidDeploymentTransition)
	require.Equal(t, "succeeded", f.awaitRunStatus(t, jsvc, runID))
}

// awaitRunStatus polls until the run is terminal.
func (f *fixture) awaitRunStatus(t *testing.T, jsvc *journal.Service, runID uuid.UUID) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		tree, err := jsvc.RunTree(context.Background(), runID)
		require.NoError(t, err)
		if tree.Run.Status != "running" && tree.Run.Status != "pending" {
			return tree.Run.Status
		}
		require.True(t, time.Now().Before(deadline), "run still %s", tree.Run.Status)
		time.Sleep(10 * time.Millisecond)
	}
}

// Closing a window (fail, cancel) lands fully even when the caller's
// context is already gone.
func TestCloseDeploymentSurvivesCancelledContext(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	jsvc := journal.NewService(f.st, "executor-1")
	definitionVersion := f.submit(t, testManifest, 0)
	deployment := f.openForComplete(t, jsvc, f.environmentID, definitionVersion, "close-plant-value")

	dead, cancel := context.WithCancel(ctx)
	cancel()
	require.NoError(t, f.deploy.FailDeployment(dead, deployment.ID, jsvc, "the local build failed"))

	row, err := f.deploy.GetDeployment(ctx, deployment.ID)
	require.NoError(t, err)
	require.Equal(t, string(DeploymentFailed), row.Status)
	tree, err := jsvc.RunTree(ctx, *deployment.RunID)
	require.NoError(t, err)
	require.Equal(t, "failed", tree.Run.Status)
}

// serverOwned closes the artifacts step of a deployment's run the way
// Complete does, without starting the completion: the state a daemon that
// died right after the hand-off leaves behind.
func (f *fixture) serverOwned(t *testing.T, jsvc *journal.Service, deployment *store.Deployment) {
	t.Helper()
	ctx := context.Background()
	tree, err := jsvc.RunTree(ctx, *deployment.RunID)
	require.NoError(t, err)
	var artifactsStep *store.Step
	for _, step := range tree.Steps {
		if step.Step.Key == "artifacts" {
			artifactsStep = &step.Step
		}
	}
	require.NotNil(t, artifactsStep)
	if artifactsStep.Status == "pending" {
		require.NoError(t, jsvc.SetStepStatus(ctx, artifactsStep.ID, journal.StepRunning))
	}
	require.NoError(t, jsvc.SetStepStatus(ctx, artifactsStep.ID, journal.StepSucceeded))
}

// stepKeys lists the keys of a run's steps.
func stepKeys(t *testing.T, jsvc *journal.Service, runID uuid.UUID) []string {
	t.Helper()
	tree, err := jsvc.RunTree(context.Background(), runID)
	require.NoError(t, err)
	var keys []string
	for _, step := range tree.Steps {
		keys = append(keys, step.Step.Key)
	}
	return keys
}

// A daemon that dies mid-completion leaves a preparing deployment whose
// artifacts step already succeeded; boot recovery resumes exactly those,
// and the resumed completion promotes as the first one would have.
func TestRecoverOnBootResumesServerOwnedDeployments(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	jsvc := journal.NewService(f.st, "executor-1")

	staging, err := f.projects.CreateEnvironment(ctx, f.projectID, "staging", project.EnvironmentOptions{})
	require.NoError(t, err)
	definitionVersion := f.submit(t, testManifest, 0)
	owned := f.openForComplete(t, jsvc, f.environmentID, definitionVersion, "owned-plant-value")
	clientSide := f.openForComplete(t, jsvc, staging.ID, definitionVersion, "client-plant-value")
	f.serverOwned(t, jsvc, owned)

	next := journal.NewService(f.st, "executor-2")
	_, err = next.RecoverOnBoot(ctx)
	require.NoError(t, err)
	resumed, failed, err := f.deploy.RecoverOnBoot(ctx, next)
	require.NoError(t, err)
	require.Equal(t, 1, resumed)
	require.Zero(t, failed)

	require.Equal(t, string(DeploymentPromoted), f.awaitDeploymentStatus(t, owned.ID))
	require.Equal(t, "succeeded", f.awaitRunStatus(t, next, *owned.RunID), "no kernel wired: promotion concludes the run")
	row, err := f.deploy.GetDeployment(ctx, owned.ID)
	require.NoError(t, err)
	target, err := f.deploy.Target(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, *row.RevisionID, *target.TargetRevisionID)
	require.Contains(t, stepKeys(t, next, *owned.RunID), resumeStepKey)
	require.NotContains(t, stepKeys(t, next, *owned.RunID), "restart")

	// The client-side window is untouched: its client may still be building.
	row, err = f.deploy.GetDeployment(ctx, clientSide.ID)
	require.NoError(t, err)
	require.Equal(t, string(DeploymentPreparing), row.Status)
	tree, err := jsvc.RunTree(ctx, *clientSide.RunID)
	require.NoError(t, err)
	require.Equal(t, "running", tree.Run.Status)

	resumed, failed, err = f.deploy.RecoverOnBoot(ctx, next)
	require.NoError(t, err)
	require.Zero(t, resumed+failed)
}

// Shutdown that outlasts the drain stops a completion between stages
// without failing anything; the next daemon resumes it to promotion.
func TestCompletionInterruptedByShutdownResumesOnBoot(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	lifetime, shutdown := context.WithCancel(ctx)
	defer shutdown()
	f.deploy.SetLifetime(lifetime)
	jsvc := journal.NewService(f.st, "executor-1")
	definitionVersion := f.submit(t, testManifest, 0)
	deployment := f.openForComplete(t, jsvc, f.environmentID, definitionVersion, "interrupted-plant-value")

	// Promotion waits on a lock the kernel holds when shutdown begins.
	unlock, err := f.st.LockEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	_, err = f.deploy.Complete(ctx, deployment.ID, jsvc)
	require.NoError(t, err)

	drainCtx, cancelDrain := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancelDrain()
	require.ErrorIs(t, f.deploy.Drain(drainCtx), context.DeadlineExceeded)
	_, err = f.deploy.Complete(ctx, deployment.ID, jsvc)
	require.ErrorIs(t, err, ErrShuttingDown, "a draining daemon starts no completion")
	shutdown()
	require.NoError(t, f.deploy.Drain(ctx), "the interrupted completion returns")
	unlock()

	row, err := f.deploy.GetDeployment(ctx, deployment.ID)
	require.NoError(t, err)
	require.Equal(t, string(DeploymentPreparing), row.Status, "interrupted, not failed")
	tree, err := jsvc.RunTree(ctx, *deployment.RunID)
	require.NoError(t, err)
	require.Equal(t, "running", tree.Run.Status)
	var staged int
	require.NoError(t, f.st.Pool.QueryRow(ctx,
		"SELECT count(*) FROM environment_secrets WHERE state = 'staged'").Scan(&staged))
	require.NotZero(t, staged, "the staged candidate survives for the resume")

	// The next daemon: a fresh service and executor over the same rows.
	next := New(f.st, f.values, f.artifacts, "test")
	nextJournal := journal.NewService(f.st, "executor-2")
	_, err = nextJournal.RecoverOnBoot(ctx)
	require.NoError(t, err)
	resumed, failed, err := next.RecoverOnBoot(ctx, nextJournal)
	require.NoError(t, err)
	require.Equal(t, 1, resumed)
	require.Zero(t, failed)
	require.Equal(t, string(DeploymentPromoted), f.awaitDeploymentStatus(t, deployment.ID))
	require.Equal(t, "succeeded", f.awaitRunStatus(t, nextJournal, *deployment.RunID))
}

// Drain waits for a running completion to finish on its own.
func TestDrainWaitsForRunningCompletion(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	jsvc := journal.NewService(f.st, "executor-1")
	definitionVersion := f.submit(t, testManifest, 0)
	deployment := f.openForComplete(t, jsvc, f.environmentID, definitionVersion, "drain-plant-value")

	unlock, err := f.st.LockEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	_, err = f.deploy.Complete(ctx, deployment.ID, jsvc)
	require.NoError(t, err)
	time.AfterFunc(200*time.Millisecond, unlock)

	drainCtx, cancelDrain := context.WithTimeout(ctx, 10*time.Second)
	defer cancelDrain()
	require.NoError(t, f.deploy.Drain(drainCtx))
	row, err := f.deploy.GetDeployment(ctx, deployment.ID)
	require.NoError(t, err)
	require.Equal(t, string(DeploymentPromoted), row.Status, "drained after promotion, not before")
}

// A completion is resumed once: a run that already carries the resume step
// was interrupted again, and boot recovery fails it instead of looping.
func TestRecoverOnBootFailsSecondInterruption(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	jsvc := journal.NewService(f.st, "executor-1")
	definitionVersion := f.submit(t, testManifest, 0)
	deployment := f.openForComplete(t, jsvc, f.environmentID, definitionVersion, "twice-plant-value")
	f.serverOwned(t, jsvc, deployment)
	require.NoError(t, f.deploy.instantStep(ctx, jsvc, *deployment.RunID, nil, resumeStepKey, "Daemon restarted", "resuming"))

	resumed, failed, err := f.deploy.RecoverOnBoot(ctx, journal.NewService(f.st, "executor-2"))
	require.NoError(t, err)
	require.Zero(t, resumed)
	require.Equal(t, 1, failed)

	row, err := f.deploy.GetDeployment(ctx, deployment.ID)
	require.NoError(t, err)
	require.Equal(t, string(DeploymentFailed), row.Status)
	tree, err := jsvc.RunTree(ctx, *deployment.RunID)
	require.NoError(t, err)
	require.Equal(t, "failed", tree.Run.Status)
	require.NotNil(t, tree.Run.Failure)
	require.Contains(t, *tree.Run.Failure, "restarted twice")
	require.Contains(t, stepKeys(t, jsvc, *deployment.RunID), "restart")
	target, err := f.deploy.Target(ctx, f.environmentID)
	require.NoError(t, err)
	require.Nil(t, target.TargetRevisionID, "nothing promoted")
}

// A deployment whose artifacts are gone by the time the daemon is back
// cannot resume; boot recovery fails it and names them.
func TestRecoverOnBootFailsWithoutVerifiedArtifacts(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	jsvc := journal.NewService(f.st, "executor-1")
	definitionVersion := f.submit(t, testManifest, 0)
	deployment := f.openForComplete(t, jsvc, f.environmentID, definitionVersion, "evicted-plant-value")
	f.serverOwned(t, jsvc, deployment)
	_, err := f.st.Pool.Exec(ctx, "UPDATE artifacts SET phase = 'evicted'")
	require.NoError(t, err)

	resumed, failed, err := f.deploy.RecoverOnBoot(ctx, journal.NewService(f.st, "executor-2"))
	require.NoError(t, err)
	require.Zero(t, resumed)
	require.Equal(t, 1, failed)
	row, err := f.deploy.GetDeployment(ctx, deployment.ID)
	require.NoError(t, err)
	require.Equal(t, string(DeploymentFailed), row.Status)
	tree, err := jsvc.RunTree(ctx, *deployment.RunID)
	require.NoError(t, err)
	require.NotNil(t, tree.Run.Failure)
	require.Contains(t, *tree.Run.Failure, "no longer verified")
}
