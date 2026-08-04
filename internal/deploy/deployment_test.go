package deploy

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/artifactstore"
	"github.com/Hinkolas/skali/internal/lifecycle"
	"github.com/Hinkolas/skali/internal/store"
)

func TestDeploymentMachine(t *testing.T) {
	t.Parallel()
	require.NoError(t, lifecycle.Verify(DeploymentStatuses))
	require.Equal(t, DeploymentPreparing, DeploymentStatuses.Initial())

	// preparing is the only live state; every terminal is final.
	require.True(t, DeploymentStatuses.Can(DeploymentPreparing, DeploymentPromoted))
	require.True(t, DeploymentStatuses.Can(DeploymentPreparing, DeploymentFailed))
	require.True(t, DeploymentStatuses.Can(DeploymentPreparing, DeploymentCancelled))
	require.False(t, DeploymentStatuses.Can(DeploymentPromoted, DeploymentFailed))
	require.False(t, DeploymentStatuses.Can(DeploymentFailed, DeploymentPreparing))
	require.True(t, DeploymentStatuses.Terminal(DeploymentPromoted))
	require.True(t, DeploymentStatuses.Terminal(DeploymentCancelled))
}

func TestDeploymentRowLifecycle(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	definitionVersion := f.submit(t, testManifest, 0)

	created, err := f.deploy.CreateDeployment(ctx, NewDeployment{
		ProjectID:           f.projectID,
		EnvironmentID:       f.environmentID,
		DefinitionVersionID: definitionVersion,
		Actor:               "tester",
	})
	require.NoError(t, err)
	require.Equal(t, string(DeploymentPreparing), created.Status)
	require.Equal(t, "local", created.BuildExecutor)

	// The partial unique index admits one preparing deployment per
	// environment.
	_, err = f.deploy.CreateDeployment(ctx, NewDeployment{
		ProjectID:           f.projectID,
		EnvironmentID:       f.environmentID,
		DefinitionVersionID: definitionVersion,
		Actor:               "tester",
	})
	require.ErrorIs(t, err, ErrDeploymentInFlight)

	// Closing the window admits the next deployment.
	require.NoError(t, f.deploy.setDeploymentStatus(ctx, created.ID, DeploymentFailed, uuid.Nil))
	got, err := f.deploy.GetDeployment(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, string(DeploymentFailed), got.Status)

	// Terminal states are final.
	err = f.deploy.setDeploymentStatus(ctx, created.ID, DeploymentPromoted, uuid.Nil)
	require.ErrorIs(t, err, ErrInvalidDeploymentTransition)

	second, err := f.deploy.CreateDeployment(ctx, NewDeployment{
		ProjectID:           f.projectID,
		EnvironmentID:       f.environmentID,
		DefinitionVersionID: definitionVersion,
		Actor:               "tester",
	})
	require.NoError(t, err)
	require.NotEqual(t, created.ID, second.ID)

	_, err = f.deploy.GetDeployment(ctx, uuid.New())
	require.ErrorIs(t, err, ErrDeploymentNotFound)
}

// TestFallbackEnvironmentTarget proves the fallback compare-and-swap: it
// returns the target to the active revision exactly once, never clobbers a
// newer target, and is a no-op before any activation.
func TestFallbackEnvironmentTarget(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	resolver := &artifactstore.Fake{Store: f.artifacts, ProjectID: f.projectID}

	promote := func(manifest string, expected int64, candidateID uuid.UUID) uuid.UUID {
		definitionVersion := f.submit(t, manifest, expected)
		prepared, err := f.deploy.Prepare(ctx, PrepareInput{
			EnvironmentID:       f.environmentID,
			DefinitionVersionID: definitionVersion,
			CandidateID:         candidateID,
			Resolver:            resolver,
		})
		require.NoError(t, err)
		require.NoError(t, f.deploy.Promote(ctx, prepared))
		return prepared.RevisionID
	}

	candidate := f.stage(t,
		map[string]string{"APP_DOMAIN": "demo.example.com", "SESSION_SECRET": "fallback-plant-value"})
	revA := promote(testManifest, 0, candidate.ID)

	// Before any activation there is nothing to fall back to.
	rows, err := f.st.FallbackEnvironmentTarget(ctx, store.FallbackEnvironmentTargetParams{
		EnvironmentID:    f.environmentID,
		TargetRevisionID: &revA,
	})
	require.NoError(t, err)
	require.Zero(t, rows)

	// Activate A, then promote B on top of it.
	activated, err := f.st.SetEnvironmentActiveRevision(ctx, store.SetEnvironmentActiveRevisionParams{
		EnvironmentID:    f.environmentID,
		ActiveRevisionID: &revA,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, activated)
	revB := promote(changedManifest, 1, uuid.Nil)
	require.NotEqual(t, revA, revB)

	// A stale expected target never moves the pointer.
	rows, err = f.st.FallbackEnvironmentTarget(ctx, store.FallbackEnvironmentTargetParams{
		EnvironmentID:    f.environmentID,
		TargetRevisionID: &revA,
	})
	require.NoError(t, err)
	require.Zero(t, rows)

	// The matching expected target falls back to the active revision.
	rows, err = f.st.FallbackEnvironmentTarget(ctx, store.FallbackEnvironmentTargetParams{
		EnvironmentID:    f.environmentID,
		TargetRevisionID: &revB,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)
	target, err := f.deploy.Target(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, revA, *target.TargetRevisionID)
	require.Equal(t, revA, *target.ActiveRevisionID)

	// Falling back twice is a no-op: the target already left revB.
	rows, err = f.st.FallbackEnvironmentTarget(ctx, store.FallbackEnvironmentTargetParams{
		EnvironmentID:    f.environmentID,
		TargetRevisionID: &revB,
	})
	require.NoError(t, err)
	require.Zero(t, rows)
}
