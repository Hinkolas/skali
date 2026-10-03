package backup

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/store"
)

func TestSnapshotsFollowRenamedEnvironment(t *testing.T) {
	f := newServiceFixture(t)
	ctx := context.Background()
	// Model an installation upgraded from a name-based namespace.
	_, err := f.st.Pool.Exec(ctx, "UPDATE environments SET backup_namespace = 'production' WHERE id = $1", f.environmentID)
	require.NoError(t, err)
	target := newMemoryStore()
	f.controller.openStore = func(s3Location) (objectStore, error) { return target, nil }
	_, err = f.controller.deps.Targets.Upsert(ctx, TargetInput{Name: DefaultTargetName, Endpoint: "https://backup.example.test", Bucket: "backups", AccessKeyID: "ak", SecretAccessKey: "sk"})
	require.NoError(t, err)
	m := snapshotManifest("before-rename", "production", TriggerScheduled, time.Now())
	writeSnapshot(t, target, m)
	_, err = project.New(f.st).RenameEnvironment(ctx, f.environmentID, "kilohertz")
	require.NoError(t, err)
	snapshots, err := f.controller.ListSnapshots(ctx, "demo", "kilohertz")
	require.NoError(t, err)
	require.Len(t, snapshots, 1)
	require.Equal(t, "kilohertz", snapshots[0].Environment)
	require.Equal(t, f.environmentID.String(), snapshots[0].EnvironmentID)
	require.False(t, snapshots[0].Orphaned)
	key, owner, err := f.controller.findSnapshot(ctx, target, "", "demo", "before-rename")
	require.NoError(t, err)
	require.Equal(t, manifestKey("", "demo", "production", "before-rename"), key)
	require.Equal(t, "kilohertz", owner)
	row, err := f.st.CreateBackup(ctx, store.CreateBackupParams{ID: uuid.New(), Kind: KindBackup, EnvironmentID: f.environmentID, ProjectName: "demo", EnvironmentName: "kilohertz", Trigger: TriggerManual, Strategy: StrategyComplete})
	require.NoError(t, err)
	require.Equal(t, "production", backupNamespace(&row), "new snapshots retain the old storage namespace")

	// Delete/recreate the display name: old snapshots remain orphaned and
	// do not become accessible through the replacement environment.
	require.NoError(t, project.New(f.st).DeleteEnvironment(ctx, f.environmentID))
	replacement, err := project.New(f.st).CreateEnvironment(ctx, f.projectID, "production", project.EnvironmentOptions{})
	require.NoError(t, err)
	require.Equal(t, replacement.ID.String(), replacement.BackupNamespace)
	snapshots, err = f.controller.ListSnapshots(ctx, "demo", "production")
	require.NoError(t, err)
	require.Empty(t, snapshots)
	snapshots, err = f.controller.ListProjectSnapshots(ctx, "demo")
	require.NoError(t, err)
	require.Len(t, snapshots, 1)
	require.True(t, snapshots[0].Orphaned)
	_, owner, err = f.controller.findSnapshot(ctx, target, "", "demo", "before-rename")
	require.NoError(t, err)
	require.Empty(t, owner, "an orphan must be subject to project-admin access checks")
	// A fresh installation still discovers self-describing snapshots from
	// S3 alone, without copying or rewriting the old manifest.
	f.controller.deps.Store = nil
	snapshots, err = f.controller.ListProjectSnapshots(ctx, "demo")
	require.NoError(t, err)
	require.Len(t, snapshots, 1)
	data, err := encodeManifest(m)
	require.NoError(t, err)
	r, err := target.Get(ctx, key)
	require.NoError(t, err)
	actual, err := readAll(r, maxManifestBytes)
	require.NoError(t, err)
	require.True(t, bytes.Equal(data, actual), "rename never rewrites snapshots")
}
