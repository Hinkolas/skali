package backup

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate"
)

// identityProbe stands in for the substrate while its platform identity
// is (pending) or is not (nil) still missing.
type identityProbe struct {
	pending error
	fenced  bool
}

func (p *identityProbe) PlatformBucketAccess(context.Context, uuid.UUID, string) (substrate.BucketAccess, error) {
	return substrate.BucketAccess{}, p.pending
}
func (p *identityProbe) PlatformIdentityReady(context.Context) error { return p.pending }
func (p *identityProbe) FenceBucket(context.Context, uuid.UUID, string) error {
	p.fenced = true
	return nil
}
func (*identityProbe) UnfenceBucket(context.Context, uuid.UUID, string) error { return nil }

func pendingIdentity() *identityProbe {
	return &identityProbe{pending: fmt.Errorf("substrate: %w", substrate.ErrPlatformIdentityPending)}
}

func bucketDefinition() compiler.ProjectDefinition {
	return compiler.ProjectDefinition{
		Schema: compiler.DefinitionSchema, Name: "demo",
		Buckets: map[string]compiler.BucketClaim{"uploads": {}},
	}
}

// Right after an upgrade the platform identity may not exist yet: a backup
// of an environment with buckets is refused before a run exists, one
// without buckets is not held up, and the refusal clears with the identity.
func TestCreateBackupWaitsForObjectStorage(t *testing.T) {
	f := newServiceFixture(t)
	ctx := context.Background()
	f.configureTarget(t)
	probe := pendingIdentity()
	f.controller.deps.Buckets = probe

	f.activate(t, bucketDefinition())
	_, err := f.controller.CreateBackup(ctx, BackupInput{EnvironmentID: f.environmentID, Actor: "tester"})
	require.ErrorIs(t, err, ErrObjectStorageNotReady)
	require.ErrorIs(t, err, substrate.ErrPlatformIdentityPending)
	runs, err := f.journal.ListRuns(ctx, f.environmentID)
	require.NoError(t, err)
	require.Empty(t, runs, "a refused backup leaves no run behind")

	probe.pending = nil
	_, err = f.controller.CreateBackup(ctx, BackupInput{EnvironmentID: f.environmentID, Actor: "tester"})
	require.NoError(t, err)
}

func TestCreateBackupWithoutBucketsIgnoresObjectStorage(t *testing.T) {
	f := newServiceFixture(t)
	f.configureTarget(t)
	f.controller.deps.Buckets = pendingIdentity()
	f.activate(t, databaseDefinition())

	_, err := f.controller.CreateBackup(context.Background(), BackupInput{EnvironmentID: f.environmentID, Actor: "tester"})
	require.NoError(t, err)
}

// A restore is refused the same way, before it could fence a bucket it
// cannot write. Without buckets it proceeds to the snapshot lookup.
func TestCreateRestoreWaitsForObjectStorage(t *testing.T) {
	f := newServiceFixture(t)
	ctx := context.Background()
	f.configureTarget(t)
	f.controller.deps.Buckets = pendingIdentity()
	f.controller.openStore = func(s3Location) (objectStore, error) { return newMemoryStore(), nil }

	f.activate(t, bucketDefinition())
	_, err := f.controller.CreateRestore(ctx, RestoreInput{EnvironmentID: f.environmentID, SnapshotID: uuid.NewString(), Actor: "tester"})
	require.ErrorIs(t, err, ErrObjectStorageNotReady)
	runs, err := f.journal.ListRuns(ctx, f.environmentID)
	require.NoError(t, err)
	require.Empty(t, runs, "a refused restore leaves no run behind")

	f.activate(t, databaseDefinition())
	_, err = f.controller.CreateRestore(ctx, RestoreInput{EnvironmentID: f.environmentID, SnapshotID: uuid.NewString(), Actor: "tester"})
	require.ErrorIs(t, err, ErrSnapshotNotFound)
}

// Platform access is resolved before the fence goes up, so a restore that
// cannot write leaves the bucket's own credentials working.
func TestRestoreBucketDoesNotFenceWithoutPlatformAccess(t *testing.T) {
	probe := pendingIdentity()
	c := &Controller{deps: Deps{Buckets: probe}, openBucket: func(context.Context, uuid.UUID, string) (objectStore, string, error) {
		return nil, "", probe.pending
	}}
	err := c.restoreBucket(context.Background(), &quietLog{}, &backupContext{row: &store.Backup{EnvironmentID: uuid.New()}, target: newMemoryStore()},
		Component{ServiceKey: "uploads", ObjectPrefix: "snapshot/", ObjectCount: 1})
	require.True(t, errors.Is(err, substrate.ErrPlatformIdentityPending))
	require.False(t, probe.fenced)
}

// A scheduled fire waits out a converging object store like a busy
// environment: the schedule does not advance and the next tick catches up.
func TestSchedulerHoldsFireWhileObjectStorageConverges(t *testing.T) {
	f := newServiceFixture(t)
	ctx := context.Background()
	f.configureTarget(t)
	probe := pendingIdentity()
	f.controller.deps.Buckets = probe
	f.activate(t, bucketDefinition())
	f.setSchedule(t, "0 3 * * *", week)
	now := time.Date(2026, 9, 16, 2, 0, 0, 0, time.UTC)
	s := newTestScheduler(f, &now)
	require.NoError(t, s.Tick(ctx))
	seeded := now

	now = now.Add(2 * time.Hour)
	require.NoError(t, s.Tick(ctx))
	unfinished, err := f.st.ListUnfinishedBackups(ctx)
	require.NoError(t, err)
	require.Empty(t, unfinished)
	rows, err := f.st.ListBackupSchedules(ctx)
	require.NoError(t, err)
	require.True(t, seeded.Equal(rows[0].LastFireAt), "a held fire does not advance the schedule")

	probe.pending = nil
	now = now.Add(time.Minute)
	require.NoError(t, s.Tick(ctx))
	unfinished, err = f.st.ListUnfinishedBackups(ctx)
	require.NoError(t, err)
	require.Len(t, unfinished, 1)
}
