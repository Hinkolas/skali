package backup

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/store"
)

// snapshotManifest builds a manifest with one database and one bucket
// component keyed under the standard layout for project demo.
func snapshotManifest(id, environment, trigger string, created time.Time) *Manifest {
	return &Manifest{
		FormatVersion:    ManifestFormatVersion,
		SnapshotID:       id,
		Encryption:       EncryptionNone,
		CreatedAt:        created,
		Project:          "demo",
		Environment:      environment,
		RevisionChecksum: "checksum",
		Trigger:          trigger,
		Strategy:         StrategyComplete,
		Components: []Component{
			{Kind: ComponentDatabase, ServiceKey: "data", Bytes: 100, Status: "complete",
				ObjectKey: databaseKey("", "demo", environment, "data", id)},
			{Kind: ComponentBucket, ServiceKey: "files", Bytes: 50, ObjectCount: 2, Status: "complete",
				ObjectPrefix: bucketPrefixKey("", "demo", environment, "files", id)},
		},
	}
}

// writeSnapshot puts a manifest and its component objects into the store.
func writeSnapshot(t *testing.T, target *memoryStore, m *Manifest) {
	t.Helper()
	ctx := context.Background()
	for _, component := range m.Components {
		switch {
		case component.ObjectKey != "":
			require.NoError(t, target.Put(ctx, component.ObjectKey, bytes.NewReader([]byte("dump")), 4))
		case component.ObjectPrefix != "":
			require.NoError(t, target.Put(ctx, component.ObjectPrefix+"a.txt", bytes.NewReader([]byte("a")), 1))
			require.NoError(t, target.Put(ctx, component.ObjectPrefix+"b.txt", bytes.NewReader([]byte("b")), 1))
		}
	}
	data, err := encodeManifest(m)
	require.NoError(t, err)
	require.NoError(t, target.Put(ctx, manifestKey("", m.Project, m.Environment, m.SnapshotID), bytes.NewReader(data), int64(len(data))))
}

func TestRetentionCandidates(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	oldManual := snapshotManifest("manual-old", "production", TriggerManual, now.Add(-30*day))
	newest := snapshotManifest("daily-newest", "production", TriggerScheduled, now.Add(-10*day))
	protectedOld := snapshotManifest("daily-restoring", "production", TriggerScheduled, now.Add(-25*day))
	oldA := snapshotManifest("daily-old-a", "production", TriggerScheduled, now.Add(-26*day))
	oldB := snapshotManifest("daily-old-b", "production", TriggerScheduled, now.Add(-40*day))
	legacy := snapshotManifest("legacy", "production", "", now.Add(-90*day))
	legacy.Trigger = TriggerManual // what decodeManifest does for pre-field manifests

	candidates := retentionCandidates(
		[]*Manifest{oldManual, oldB, newest, protectedOld, oldA, legacy},
		now.Add(-7*day), map[string]bool{"daily-restoring": true})

	ids := make([]string, 0, len(candidates))
	for _, c := range candidates {
		ids = append(ids, c.SnapshotID)
	}
	require.ElementsMatch(t, []string{"daily-old-a", "daily-old-b"}, ids,
		"manual, newest, protected and legacy snapshots are never candidates")

	// Even a window shorter than the gap to the newest keeps the newest.
	single := retentionCandidates([]*Manifest{newest}, now, nil)
	require.Empty(t, single)

	// A snapshot inside the window is kept even when it is not the newest.
	fresh := snapshotManifest("daily-fresh", "production", TriggerScheduled, now.Add(-3*day))
	fresher := snapshotManifest("daily-fresher", "production", TriggerScheduled, now.Add(-day))
	inside := retentionCandidates([]*Manifest{fresh, fresher, oldA}, now.Add(-7*day), nil)
	require.Len(t, inside, 1)
	require.Equal(t, "daily-old-a", inside[0].SnapshotID)
}

func TestDeleteSnapshotObjectsRemovesManifestFirst(t *testing.T) {
	target := newMemoryStore()
	m := snapshotManifest("snap", "production", TriggerManual, time.Now())
	writeSnapshot(t, target, m)
	target.ops = nil

	key := manifestKey("", "demo", "production", "snap")
	require.NoError(t, deleteSnapshotObjects(context.Background(), target, key, m))
	require.Equal(t, []string{
		"remove " + key,
		"remove " + m.Components[0].ObjectKey,
		"remove-prefix " + m.Components[1].ObjectPrefix,
	}, target.ops)
	require.Empty(t, target.keys(""), "every object of the snapshot is gone")

	// Deleting again is idempotent: missing objects are not errors.
	require.NoError(t, deleteSnapshotObjects(context.Background(), target, key, m))
}

// applyRetention against the fake target: the two expired scheduled
// snapshots go, everything else stays (including a snapshot a manifest
// backup policy of an earlier release took, which reads as manual), and
// the step log explains the sweep.
func TestApplyRetentionSweepsOnlyExpiredScheduledSnapshots(t *testing.T) {
	f := newServiceFixture(t)
	ctx := context.Background()
	target := newMemoryStore()
	f.controller.openStore = func(s3Location) (objectStore, error) { return target, nil }

	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	policyEra := snapshotManifest("policy-era", "production", TriggerScheduled, now.Add(-30*day))
	policyEra.Policy = "daily"
	manifests := []*Manifest{
		snapshotManifest("manual-old", "production", TriggerManual, now.Add(-30*day)),
		policyEra,
		snapshotManifest("daily-old-a", "production", TriggerScheduled, now.Add(-9*day)),
		snapshotManifest("daily-old-b", "production", TriggerScheduled, now.Add(-8*day)),
		snapshotManifest("daily-kept", "production", TriggerScheduled, now.Add(-2*day)),
		snapshotManifest("daily-newest", "production", TriggerScheduled, now),
		snapshotManifest("staging-old", "staging", TriggerScheduled, now.Add(-30*day)),
	}
	for _, m := range manifests {
		writeSnapshot(t, target, m)
	}

	run, err := f.journal.CreateRun(ctx, journal.RunInput{
		Kind: KindBackup, ProjectID: f.projectID, EnvironmentID: f.environmentID, Actor: ScheduleActor,
	})
	require.NoError(t, err)
	require.NoError(t, f.journal.StartRun(ctx, run.ID))
	redactor, err := f.controller.deps.Values.Redactor(ctx, f.environmentID, uuid.Nil)
	require.NoError(t, err)
	scope := &runScope{journal: f.journal, redactor: redactor, runID: run.ID}
	row := &store.Backup{ProjectName: "demo", EnvironmentName: "production", Trigger: TriggerScheduled, RetentionSeconds: 7 * 86400}
	bctx := &backupContext{row: row, credentials: &Credentials{}, target: target}

	require.NoError(t, scope.step(ctx, "retention", "Apply retention", func(ctx context.Context, log *stepLog) error {
		return f.controller.applyRetention(ctx, log, bctx, 7*day, now)
	}))

	for _, id := range []string{"manual-old", "policy-era", "daily-kept", "daily-newest"} {
		require.True(t, target.has(manifestKey("", "demo", "production", id)), "%s must survive", id)
	}
	require.True(t, target.has(manifestKey("", "demo", "staging", "staging-old")), "other environments are out of scope")
	for _, id := range []string{"daily-old-a", "daily-old-b"} {
		require.False(t, target.has(manifestKey("", "demo", "production", id)), "%s must be deleted", id)
		require.False(t, target.has(databaseKey("", "demo", "production", "data", id)), "%s dump must be deleted", id)
		require.Empty(t, target.keys(bucketPrefixKey("", "demo", "production", "files", id)), "%s bucket copy must be deleted", id)
	}
}
