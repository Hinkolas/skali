package backup

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// countingStore counts manifest reads, to prove the summary's cache.
type countingStore struct {
	*memoryStore
	gets atomic.Int32
}

func (s *countingStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	s.gets.Add(1)
	return s.memoryStore.Get(ctx, key)
}

// unreachableStore is a target whose bucket does not answer.
type unreachableStore struct{ *memoryStore }

func (unreachableStore) Reachable(context.Context) error {
	return errors.New("backup: bucket backups does not exist on backup.example.test")
}

func configureTarget(t *testing.T, f *serviceFixture) {
	t.Helper()
	_, err := f.controller.deps.Targets.Upsert(context.Background(), TargetInput{
		Name: DefaultTargetName, Endpoint: "https://backup.example.test", Bucket: "backups",
		AccessKeyID: "ak", SecretAccessKey: "sk"})
	require.NoError(t, err)
}

func TestSummarizeWithoutTarget(t *testing.T) {
	f := newServiceFixture(t)
	_, err := f.controller.Summarize(context.Background())
	require.ErrorIs(t, err, ErrTargetNotFound)
}

func TestSummarizeTotalsSnapshotsAndReadsEachManifestOnce(t *testing.T) {
	f := newServiceFixture(t)
	ctx := context.Background()
	_, err := f.st.Pool.Exec(ctx, "UPDATE environments SET backup_namespace = 'production' WHERE id = $1", f.environmentID)
	require.NoError(t, err)
	configureTarget(t, f)
	target := &countingStore{memoryStore: newMemoryStore()}
	f.controller.openStore = func(s3Location) (objectStore, error) { return target, nil }

	now := time.Now().UTC().Truncate(time.Second)
	day := 24 * time.Hour
	writeSnapshot(t, target.memoryStore, snapshotManifest("nightly", "production", TriggerScheduled, now.Add(-day)))
	writeSnapshot(t, target.memoryStore, snapshotManifest("manual", "production", TriggerManual, now))
	// An environment the control plane no longer has.
	writeSnapshot(t, target.memoryStore, snapshotManifest("old", "deleted-env", TriggerManual, now.Add(-10*day)))
	shop := snapshotManifest("shop-1", "production", TriggerManual, now.Add(-2*day))
	shop.Project = "shop"
	shop.Components = shop.Components[:1]
	writeSnapshot(t, target.memoryStore, shop)

	summary, err := f.controller.Summarize(ctx)
	require.NoError(t, err)
	require.True(t, summary.Reachable)
	totals := summary.Snapshots
	require.NotNil(t, totals)
	require.Equal(t, 4, totals.Count)
	require.EqualValues(t, 3*150+100, totals.Bytes)
	require.Equal(t, 1, totals.Scheduled)
	require.Equal(t, 3, totals.Manual)
	// deleted-env in demo, and every shop snapshot (no such project).
	require.Equal(t, 2, totals.Orphaned)
	require.Equal(t, now.Add(-10*day), *totals.OldestAt)
	require.Equal(t, now, *totals.NewestAt)
	require.Len(t, totals.Projects, 2)
	require.Equal(t, ProjectSnapshotTotals{Project: "demo", Count: 3, Bytes: 450, NewestAt: now}, totals.Projects[0])
	require.Equal(t, "shop", totals.Projects[1].Project)
	require.EqualValues(t, 4, target.gets.Load())

	// Manifests never change: the next summary lists, but reads nothing.
	summary, err = f.controller.Summarize(ctx)
	require.NoError(t, err)
	require.Equal(t, 4, summary.Snapshots.Count)
	require.EqualValues(t, 4, target.gets.Load())

	// A deleted snapshot leaves the totals (and the cache).
	require.NoError(t, target.Remove(ctx, manifestKey("", "demo", "production", "manual")))
	summary, err = f.controller.Summarize(ctx)
	require.NoError(t, err)
	require.Equal(t, 3, summary.Snapshots.Count)
	require.Len(t, f.controller.manifests.summaries, 3)
}

func TestSummarizeReportsActivity(t *testing.T) {
	f := newServiceFixture(t)
	ctx := context.Background()
	configureTarget(t, f)
	f.controller.openStore = func(s3Location) (objectStore, error) { return newMemoryStore(), nil }
	_, err := f.st.Pool.Exec(ctx, "UPDATE environments SET backup_schedule = '0 3 * * *' WHERE id = $1", f.environmentID)
	require.NoError(t, err)
	insert := func(kind, status string, finished *time.Time, message *string) {
		_, err := f.st.Pool.Exec(ctx, `INSERT INTO backups (id, kind, environment_id, project_name, environment_name, status, error, finished_at)
			VALUES ($1, $2, $3, 'demo', 'production', $4, $5, $6)`, uuid.New(), kind, f.environmentID, status, message, finished)
		require.NoError(t, err)
	}
	recent := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	older := time.Now().Add(-10 * 24 * time.Hour).UTC().Truncate(time.Microsecond)
	failure := "dump failed"
	insert(KindBackup, StatusSucceeded, &recent, nil)
	insert(KindBackup, StatusSucceeded, &older, nil)
	insert(KindBackup, StatusFailed, &older, &failure)
	insert(KindBackup, StatusRunning, nil, nil)
	insert(KindRestore, StatusPending, nil, nil)

	summary, err := f.controller.Summarize(ctx)
	require.NoError(t, err)
	runs := summary.Runs
	require.EqualValues(t, 1, runs.Succeeded, "the window holds the recent success only")
	require.EqualValues(t, 0, runs.Failed)
	require.EqualValues(t, 1, runs.BackingUp)
	require.EqualValues(t, 1, runs.Restoring)
	require.NotNil(t, runs.LastSucceeded)
	require.Equal(t, recent, runs.LastSucceeded.FinishedAt.UTC())
	require.Equal(t, "production", runs.LastSucceeded.Environment)
	require.NotNil(t, runs.LastFailed)
	require.Equal(t, "dump failed", runs.LastFailed.Error)
	require.Equal(t, ScheduleTotals{Environments: 1, Scheduled: 1}, summary.Schedules)
	require.NotNil(t, summary.Snapshots)
	require.Zero(t, summary.Snapshots.Count)
}

func TestSummarizeUnreachableTarget(t *testing.T) {
	f := newServiceFixture(t)
	configureTarget(t, f)
	f.controller.openStore = func(s3Location) (objectStore, error) {
		return unreachableStore{newMemoryStore()}, nil
	}
	summary, err := f.controller.Summarize(context.Background())
	require.NoError(t, err)
	require.False(t, summary.Reachable)
	require.Contains(t, summary.UnreachableReason, "does not exist")
	require.Nil(t, summary.Snapshots)
}
