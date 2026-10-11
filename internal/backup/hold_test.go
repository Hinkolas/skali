package backup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/store"
)

// holdFixture is a service fixture whose environment runs a revision with
// one bucket, "files", and whose restores run end to end without a
// cluster: the target and the bucket are in-memory stores, the fence is a
// probe, the stop finds no workloads, and resuming activates the revision
// at once, the way a converged reconcile pass would.
type holdFixture struct {
	*serviceFixture
	revisionID  uuid.UUID
	snapshots   *memoryStore
	destination *memoryStore
	fence       *fenceProbe
	// bucket is what restores write the bucket through; the destination
	// unless a test wraps it.
	bucket objectStore
}

func newHoldFixture(t *testing.T) *holdFixture {
	t.Helper()
	f := &holdFixture{serviceFixture: newServiceFixture(t), snapshots: newMemoryStore(),
		destination: newMemoryStore(), fence: &fenceProbe{}}
	f.bucket = f.destination
	f.configureTarget(t)
	f.revisionID = f.activate(t, compiler.ProjectDefinition{
		Schema: compiler.DefinitionSchema, Name: "demo",
		Buckets: map[string]compiler.BucketClaim{"files": {}},
	})
	fillStore(t, f.destination, "live/", 2)
	c := f.controller
	c.deps.Buckets = f.fence
	c.deps.Kube = &kube.Client{Clientset: fake.NewSimpleClientset()}
	c.openStore = func(s3Location) (objectStore, error) { return f.snapshots, nil }
	c.openBucket = func(context.Context, uuid.UUID, string) (objectStore, string, error) {
		return f.bucket, "files-bucket", nil
	}
	c.resume = func(ctx context.Context, environmentID, revisionID, restoreID uuid.UUID) error {
		resumed, err := f.st.ResumeRestoredEnvironment(ctx, store.ResumeRestoredEnvironmentParams{
			EnvironmentID: environmentID, TargetRevisionID: revisionID, RestoreID: restoreID,
		})
		if err != nil || resumed == 0 {
			return fmt.Errorf("resume: %d rows: %w", resumed, err)
		}
		_, err = f.st.SetEnvironmentActiveRevision(ctx, store.SetEnvironmentActiveRevisionParams{
			EnvironmentID: environmentID, ActiveRevisionID: &revisionID,
		})
		return err
	}
	return f
}

// snapshot writes a bucket snapshot of n objects and its manifest, and
// returns its id.
func (f *holdFixture) snapshot(t *testing.T, id string, n int) string {
	t.Helper()
	prefix := bucketPrefixKey("", "demo", "production", "files", id)
	fillStore(t, f.snapshots, prefix, n)
	listing, err := listBucket(context.Background(), f.snapshots, prefix)
	require.NoError(t, err)
	m := &Manifest{
		FormatVersion: ManifestFormatVersion, SnapshotID: id, Encryption: EncryptionNone,
		CreatedAt: time.Now(), Project: "demo", Environment: "production", Trigger: TriggerManual,
		Strategy: StrategyComplete,
		Components: []Component{{Kind: ComponentBucket, ServiceKey: "files", ObjectPrefix: prefix,
			ObjectCount: listing.Objects, Bytes: listing.Bytes, Status: "complete"}},
	}
	data, err := encodeManifest(m)
	require.NoError(t, err)
	require.NoError(t, f.snapshots.Put(context.Background(), manifestKey("", "demo", "production", id),
		bytes.NewReader(data), int64(len(data))))
	return id
}

// restore accepts and runs one restore of the snapshot to its end.
func (f *holdFixture) restore(t *testing.T, snapshotID string) (*CreateResult, store.Backup) {
	t.Helper()
	result, err := f.controller.CreateRestore(context.Background(), RestoreInput{
		EnvironmentID: f.environmentID, SnapshotID: snapshotID, Actor: "tester",
	})
	require.NoError(t, err)
	require.NoError(t, f.controller.process(context.Background(), result.BackupID))
	return result, f.rowStatus(t, result.BackupID)
}

// failingStore refuses every write with a verdict the copy does not retry.
type failingStore struct{ *memoryStore }

func (failingStore) PutWithMeta(context.Context, string, io.Reader, int64, objectMeta) error {
	return minio.ErrorResponse{StatusCode: 403, Code: "AccessDenied", Message: "denied"}
}

// A snapshot whose data does not match its manifest fails the restore
// before anything is stopped or overwritten: the environment keeps
// running, takes no hold, and its bucket is neither fenced nor cleared.
func TestRestoreRefusesDamagedSnapshotBeforeStopping(t *testing.T) {
	f := newHoldFixture(t)
	snapshot := f.snapshot(t, "damaged", 3)
	require.NoError(t, f.snapshots.Remove(context.Background(), bucketPrefixKey("", "demo", "production", "files", snapshot)+"object-001"))

	result, row := f.restore(t, snapshot)
	require.Equal(t, StatusFailed, row.Status)
	require.Contains(t, *row.Error, "snapshot damaged is damaged: bucket:files holds 2 objects")
	require.Contains(t, *row.Error, "nothing was changed")
	require.Equal(t, string(journal.RunFailed), f.runStatus(t, result.RunID))
	target := f.target(t)
	require.Equal(t, "active", target.State)
	require.Equal(t, f.revisionID, *target.ActiveRevisionID)
	require.Nil(t, target.IncompleteRestoreID)
	require.False(t, f.fence.fenced)
	require.True(t, f.destination.has("live/object-000"), "the live bucket was never cleared")
}

// A restore that fails after it stopped the environment holds it: down,
// with every deploy refused and the bucket fenced, also across a later
// look. A restore re-run over the hold is accepted although the
// environment is down, resumes the revision the failed one captured, and
// lifts the hold when it completes.
func TestRestoreHoldsEnvironmentUntilARestoreCompletes(t *testing.T) {
	f := newHoldFixture(t)
	ctx := context.Background()
	snapshot := f.snapshot(t, "snap", 3)

	f.bucket = failingStore{f.destination}
	failed, row := f.restore(t, snapshot)
	require.Equal(t, StatusFailed, row.Status)
	target := f.target(t)
	require.Equal(t, "down", target.State)
	require.Nil(t, target.TargetRevisionID)
	require.Equal(t, row.ID, *target.IncompleteRestoreID)
	require.Equal(t, []string{"bucket:files"}, target.IncompleteRestoreComponents)
	require.True(t, f.fence.fenced)
	require.False(t, f.fence.unfenced, "a bucket a failed restore rewrote stays fenced")
	require.True(t, f.slotFree(t), "the failed run frees the run slot")

	moved, err := f.st.SetEnvironmentTarget(ctx, store.SetEnvironmentTargetParams{
		EnvironmentID: f.environmentID, TargetRevisionID: &f.revisionID,
	})
	require.NoError(t, err)
	require.Zero(t, moved, "no deployment moves the target of a held environment")
	_, err = f.controller.CreateBackup(ctx, BackupInput{EnvironmentID: f.environmentID, Actor: "tester"})
	require.ErrorIs(t, err, ErrEnvironmentNotActive)

	f.bucket = f.destination
	again, row := f.restore(t, snapshot)
	require.NotEqual(t, failed.RunID, again.RunID)
	require.Equal(t, StatusSucceeded, row.Status, "error: %v", row.Error)
	require.Equal(t, f.revisionID, *row.RevisionID, "the re-run resumes the revision the failed restore captured")
	target = f.target(t)
	require.Equal(t, "active", target.State)
	require.Equal(t, f.revisionID, *target.TargetRevisionID)
	require.Nil(t, target.IncompleteRestoreID)
	require.Empty(t, target.IncompleteRestoreComponents)
	require.True(t, f.fence.unfenced)
	require.False(t, f.destination.has("live/object-000"))
	require.True(t, f.destination.has("object-002"))
}

// A restore re-run over a hold must rewrite every component the incomplete
// restore set out to rewrite: lifting the hold on one that leaves a
// half-written component alone would bring the environment back on
// partial data after all. It fails before it stops anything, and the
// original hold stays.
func TestRestoreRerunMustCoverTheHeldComponents(t *testing.T) {
	f := newHoldFixture(t)
	ctx := context.Background()
	heldRun, held := f.beginRow(t, KindRestore, &f.revisionID)
	_, err := f.st.SetBackupStatus(ctx, store.SetBackupStatusParams{ID: held.ID, ToStatus: StatusRunning, FromStatus: StatusPending})
	require.NoError(t, err)
	marked, err := f.st.MarkEnvironmentRestoring(ctx, store.MarkEnvironmentRestoringParams{
		EnvironmentID: f.environmentID, RestoreID: held.ID, Components: []string{"db:data", "bucket:files"},
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, marked)
	require.NoError(t, f.controller.failRow(ctx, held.ID, "the dump failed halfway"))
	require.NoError(t, f.journal.FinishRun(ctx, heldRun, journal.RunFailed))

	_, row := f.restore(t, f.snapshot(t, "bucket-only", 2))
	require.Equal(t, StatusFailed, row.Status)
	require.Contains(t, *row.Error, "also rewrote db:data, which snapshot bucket-only does not restore")
	target := f.target(t)
	require.Equal(t, held.ID, *target.IncompleteRestoreID)
	require.Equal(t, []string{"db:data", "bucket:files"}, target.IncompleteRestoreComponents)
	require.False(t, f.fence.fenced)
}

// A daemon restart in the middle of a restore fails the row, and the hold
// the restore took when it stopped the environment stays: the restart
// cannot have completed the data. The failure says so.
func TestRecoverOnBootKeepsTheHold(t *testing.T) {
	f := newHoldFixture(t)
	ctx := context.Background()
	runID, row := f.beginRow(t, KindRestore, &f.revisionID)
	_, err := f.st.SetBackupStatus(ctx, store.SetBackupStatusParams{ID: row.ID, ToStatus: StatusRunning, FromStatus: StatusPending})
	require.NoError(t, err)
	_, err = f.st.MarkEnvironmentRestoring(ctx, store.MarkEnvironmentRestoringParams{
		EnvironmentID: f.environmentID, RestoreID: row.ID, Components: []string{"bucket:files"},
	})
	require.NoError(t, err)

	_, err = f.controller.AcceptIncompleteRestore(ctx, AcceptInput{
		EnvironmentID: f.environmentID, RestoreID: row.ID, Actor: "tester",
	})
	require.ErrorIs(t, err, ErrRestoreStillRunning, "a running restore's data is not accepted")

	require.NoError(t, f.controller.RecoverOnBoot(ctx))
	recovered := f.rowStatus(t, row.ID)
	require.Equal(t, StatusFailed, recovered.Status)
	require.Contains(t, *recovered.Error, "the environment stays down until a restore completes or its data is accepted")
	require.Equal(t, string(journal.RunFailed), f.runStatus(t, runID))
	target := f.target(t)
	require.Equal(t, "down", target.State)
	require.Equal(t, row.ID, *target.IncompleteRestoreID)
}

// cancellingBucket cancels the restore's run on its first write.
type cancellingBucket struct {
	*memoryStore
	cancel func()
}

func (s *cancellingBucket) PutWithMeta(ctx context.Context, key string, r io.Reader, size int64, meta objectMeta) error {
	s.cancel()
	<-ctx.Done()
	return ctx.Err()
}

// A restore cancelled after it stopped the environment is as incomplete as
// a failed one: the row and the run end cancelled, and the hold stays.
func TestCancelAfterStopKeepsTheHold(t *testing.T) {
	f := newHoldFixture(t)
	ctx := context.Background()
	snapshot := f.snapshot(t, "snap", 3)
	result, err := f.controller.CreateRestore(ctx, RestoreInput{EnvironmentID: f.environmentID, SnapshotID: snapshot, Actor: "tester"})
	require.NoError(t, err)
	f.bucket = &cancellingBucket{memoryStore: f.destination, cancel: func() {
		go func() { _ = f.controller.Cancel(context.Background(), result.RunID) }()
	}}

	require.NoError(t, f.controller.process(ctx, result.BackupID))
	require.Equal(t, StatusCancelled, f.rowStatus(t, result.BackupID).Status)
	require.Equal(t, string(journal.RunCancelled), f.runStatus(t, result.RunID))
	target := f.target(t)
	require.Equal(t, "down", target.State)
	require.Equal(t, result.BackupID, *target.IncompleteRestoreID)
}

// Accepting an incomplete restore's data lifts its hold and nothing else:
// the environment stays down until a deploy, which may now move the
// target. Only the restore that holds the environment can be accepted, by
// a journaled run of its own that no other run may overlap.
func TestAcceptIncompleteRestore(t *testing.T) {
	f := newHoldFixture(t)
	ctx := context.Background()
	f.bucket = failingStore{f.destination}
	_, row := f.restore(t, f.snapshot(t, "snap", 2))
	require.Equal(t, StatusFailed, row.Status)

	_, err := f.controller.AcceptIncompleteRestore(ctx, AcceptInput{
		EnvironmentID: f.environmentID, RestoreID: uuid.New(), Actor: "tester",
	})
	require.ErrorIs(t, err, ErrNoIncompleteRestore)
	busy, err := f.journal.BeginRun(ctx, journal.RunInput{
		Kind: "reconcile", ProjectID: f.projectID, EnvironmentID: f.environmentID, Actor: "tester",
	})
	require.NoError(t, err)
	_, err = f.controller.AcceptIncompleteRestore(ctx, AcceptInput{
		EnvironmentID: f.environmentID, RestoreID: row.ID, Actor: "tester",
	})
	require.ErrorIs(t, err, ErrBackupInFlight)
	require.NoError(t, f.journal.FinishRun(ctx, busy.ID, journal.RunSucceeded))

	runID, err := f.controller.AcceptIncompleteRestore(ctx, AcceptInput{
		EnvironmentID: f.environmentID, RestoreID: row.ID, Actor: "tester",
	})
	require.NoError(t, err)
	run, err := f.journal.Run(ctx, runID)
	require.NoError(t, err)
	require.Equal(t, KindAccept, run.Kind)
	require.Equal(t, string(journal.RunSucceeded), run.Status)
	tree, err := f.journal.RunTree(ctx, runID)
	require.NoError(t, err)
	require.Len(t, tree.Steps, 1)
	events, err := f.journal.StepLogs(ctx, tree.Steps[0].Step.ID, journal.Cursor{}, 10)
	require.NoError(t, err)
	var lines []string
	for _, event := range events {
		lines = append(lines, event.Level+": "+event.Message)
	}
	require.Contains(t, strings.Join(lines, "\n"), "warn: it had set out to rewrite bucket:files; any of them may hold partial data")
	target := f.target(t)
	require.Nil(t, target.IncompleteRestoreID)
	require.Equal(t, "down", target.State, "accepting does not bring the environment back")
	require.True(t, f.fence.fenced && !f.fence.unfenced, "the next deployment lifts the fence")

	_, err = f.controller.AcceptIncompleteRestore(ctx, AcceptInput{
		EnvironmentID: f.environmentID, RestoreID: row.ID, Actor: "tester",
	})
	require.ErrorIs(t, err, ErrNoIncompleteRestore)
	moved, err := f.st.SetEnvironmentTarget(ctx, store.SetEnvironmentTargetParams{
		EnvironmentID: f.environmentID, TargetRevisionID: &f.revisionID,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, moved, "a deployment brings the environment back")
}

// An accept run a dying daemon never finished would hold the
// environment's run slot for good; the stranded-run sweep fails it once
// the grace has passed, like a backup run without its row.
func TestSweepFailsStrandedAcceptRun(t *testing.T) {
	f := newServiceFixture(t)
	ctx := context.Background()
	run, err := f.journal.BeginRun(ctx, journal.RunInput{
		Kind: KindAccept, ProjectID: f.projectID, EnvironmentID: f.environmentID, Actor: "tester",
	})
	require.NoError(t, err)
	require.NoError(t, f.controller.sweep(ctx))
	require.Equal(t, string(journal.RunRunning), f.runStatus(t, run.ID), "an accept may still be finishing")
	_, err = f.st.Pool.Exec(ctx, "UPDATE runs SET created_at = now() - interval '2 minutes' WHERE id = $1", run.ID)
	require.NoError(t, err)
	require.NoError(t, f.controller.sweep(ctx))
	require.Equal(t, string(journal.RunFailed), f.runStatus(t, run.ID))
	require.True(t, f.slotFree(t))
}

// tearingBucket takes the environment down on the restore's first write
// the way a teardown does: the run is cancelled in the journal only and
// the environment marked down, while the copy goes on.
type tearingBucket struct {
	*memoryStore
	teardown sync.Once
	down     func()
}

func (s *tearingBucket) PutWithMeta(ctx context.Context, key string, r io.Reader, size int64, meta objectMeta) error {
	s.teardown.Do(s.down)
	return s.memoryStore.PutWithMeta(ctx, key, r, size, meta)
}

// A teardown that lands while a restore copies is not undone when the
// copy completes: the restore sees its run cancelled and does not resume,
// and the environment stays down, held.
func TestTeardownDuringRestoreIsNotUndone(t *testing.T) {
	f := newHoldFixture(t)
	ctx := context.Background()
	snapshot := f.snapshot(t, "snap", 3)
	result, err := f.controller.CreateRestore(ctx, RestoreInput{EnvironmentID: f.environmentID, SnapshotID: snapshot, Actor: "tester"})
	require.NoError(t, err)
	f.bucket = &tearingBucket{memoryStore: f.destination, down: func() {
		require.NoError(t, f.journal.FinishRun(ctx, result.RunID, journal.RunCancelled))
		_, err := f.st.MarkEnvironmentDown(ctx, f.environmentID)
		require.NoError(t, err)
	}}

	require.NoError(t, f.controller.process(ctx, result.BackupID))
	require.Equal(t, StatusCancelled, f.rowStatus(t, result.BackupID).Status)
	target := f.target(t)
	require.Equal(t, "down", target.State)
	require.Nil(t, target.TargetRevisionID)
	require.Equal(t, result.BackupID, *target.IncompleteRestoreID)
}
