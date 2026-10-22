package deploy

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/artifactstore"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/plan"
)

// bucketAndDatabaseManifest is bucketManifest("5GB") with a database added.
const bucketAndDatabaseManifest = `name: demo
buckets:
  media:
    quotas:
      storage: 5GB
databases:
  main:
    engine: postgres
    version: 17
    isolation: shared
    storage:
      size: 1GB
`

func volumeManifest(size string) string {
	return `name: demo
applications:
  files:
    image: example.invalid/files:1
    volumes:
      data:
        mountPath: /data
        size: ` + size + "\n"
}

// promoteRevision promotes a manifest without the open's checks, as the
// rollback tests only need revisions to roll between. active also marks
// it active, as a rollout that converged would.
func (f *fixture) promoteRevision(t *testing.T, source string, expected int64, active bool) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	version := f.submit(t, source, expected)
	prepared, err := f.deploy.Prepare(ctx, PrepareInput{
		EnvironmentID: f.environmentID, DefinitionVersionID: version,
		Resolver: &artifactstore.Fake{Store: f.artifacts, ProjectID: f.projectID},
	})
	require.NoError(t, err)
	require.NoError(t, f.deploy.Promote(ctx, prepared))
	if active {
		f.activateTarget(t)
	}
	return prepared.RevisionID
}

// Rolling back past the revision that added a database releases it with
// its data once the rollback is active, so the rollback is refused naming
// it, with nothing moved, until it is confirmed.
func TestRollbackRefusesDroppingADatabaseUnconfirmed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	jsvc := journal.NewService(f.st, "test")

	older := f.promoteRevision(t, bucketManifest("5GB"), 0, true)
	current := f.promoteRevision(t, bucketAndDatabaseManifest, 1, true)

	_, err := f.deploy.Rollback(ctx, RollbackInput{
		EnvironmentID: f.environmentID, RevisionID: older, Actor: "test", Journal: jsvc,
	})
	require.ErrorIs(t, err, ErrDestructiveChange)
	var destructive *DestructiveRollbackError
	require.ErrorAs(t, err, &destructive)
	require.Equal(t, []plan.Change{{
		Service: "databases.main", Action: plan.ActionRemove, Destructive: true,
		Detail: "deletes the logical database and its data",
	}}, destructive.Changes)
	require.Contains(t, err.Error(), "databases.main (deletes the logical database and its data)")
	f.untouched(t, &current)

	_, err = f.deploy.Rollback(ctx, RollbackInput{
		EnvironmentID: f.environmentID, RevisionID: older, Actor: "test", Journal: jsvc,
		AllowDestructive: true,
	})
	require.NoError(t, err)
	target, err := f.deploy.Target(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, older, *target.TargetRevisionID)
}

// Aborting a rollout in flight drops only what that rollout added, as its
// fallback would; the active revision is unchanged, so no confirmation is
// asked for.
func TestRollbackAbortingARolloutNeedsNoConfirmation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	jsvc := journal.NewService(f.st, "test")

	active := f.promoteRevision(t, bucketManifest("5GB"), 0, true)
	f.promoteRevision(t, bucketAndDatabaseManifest, 1, false)

	_, err := f.deploy.Rollback(ctx, RollbackInput{
		EnvironmentID: f.environmentID, RevisionID: active, Actor: "test", Journal: jsvc,
	})
	require.NoError(t, err)
	target, err := f.deploy.Target(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, active, *target.TargetRevisionID)
}

// A rollback to a smaller volume than the active revision's is refused
// before any run begins, naming the volume and both sizes; confirming
// destructive changes does not get past it, since Kubernetes rejects the
// shrink.
func TestRollbackRefusesShrinkingAVolume(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	jsvc := journal.NewService(f.st, "test")

	older := f.promoteRevision(t, volumeManifest("1GB"), 0, true)
	current := f.promoteRevision(t, volumeManifest("2GB"), 1, true)

	_, err := f.deploy.Rollback(ctx, RollbackInput{
		EnvironmentID: f.environmentID, RevisionID: older, Actor: "test", Journal: jsvc,
		AllowDestructive: true,
	})
	var shrink *VolumeShrinkError
	require.ErrorAs(t, err, &shrink)
	require.Equal(t, "files", shrink.Application)
	require.Equal(t, "data", shrink.Volume)
	require.Greater(t, shrink.FromBytes, shrink.ToBytes)
	f.untouched(t, &current)
}
