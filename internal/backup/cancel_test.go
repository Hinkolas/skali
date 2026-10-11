package backup

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/revision"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/valuestore"
)

// beginRow records a run and its pending row the way the accept paths do.
func (f *serviceFixture) beginRow(t *testing.T, kind string, revisionID *uuid.UUID) (uuid.UUID, store.Backup) {
	t.Helper()
	ctx := context.Background()
	run, err := f.journal.BeginRun(ctx, journal.RunInput{
		Kind: kind, ProjectID: f.projectID, EnvironmentID: f.environmentID, Actor: "tester",
	})
	require.NoError(t, err)
	runID := run.ID
	row, err := f.st.CreateBackup(ctx, store.CreateBackupParams{
		ID: uuid.New(), Kind: kind, EnvironmentID: f.environmentID,
		ProjectName: "demo", EnvironmentName: "production", RevisionID: revisionID, RunID: &runID,
		Trigger: TriggerManual, Strategy: StrategyComplete,
	})
	require.NoError(t, err)
	return run.ID, row
}

func (f *serviceFixture) runStatus(t *testing.T, id uuid.UUID) string {
	t.Helper()
	run, err := f.journal.Run(context.Background(), id)
	require.NoError(t, err)
	return run.Status
}

func (f *serviceFixture) rowStatus(t *testing.T, id uuid.UUID) store.Backup {
	t.Helper()
	row, err := f.st.GetBackup(context.Background(), id)
	require.NoError(t, err)
	return row
}

// slotFree reports whether the environment could begin another run.
func (f *serviceFixture) slotFree(t *testing.T) bool {
	t.Helper()
	run, err := f.journal.BeginRun(context.Background(), journal.RunInput{
		Kind: "reconcile", ProjectID: f.projectID, EnvironmentID: f.environmentID, Actor: "tester",
	})
	if err != nil {
		require.ErrorIs(t, err, journal.ErrRunConflict)
		return false
	}
	require.NoError(t, f.journal.FinishRun(context.Background(), run.ID, journal.RunCancelled))
	return true
}

// A restore cancelled while it waits never starts: its row is cancelled
// with the run, and a worker that picks it up afterwards does nothing.
func TestCancelPendingRestoreNeverStarts(t *testing.T) {
	f := newServiceFixture(t)
	ctx := context.Background()
	revisionID := f.activate(t, databaseDefinition())
	runID, row := f.beginRow(t, KindRestore, &revisionID)

	require.NoError(t, f.controller.Cancel(ctx, runID))
	require.Equal(t, string(journal.RunCancelled), f.runStatus(t, runID))
	cancelled := f.rowStatus(t, row.ID)
	require.Equal(t, StatusCancelled, cancelled.Status)
	require.NotNil(t, cancelled.FinishedAt)
	require.True(t, f.slotFree(t))

	require.NoError(t, f.controller.process(ctx, row.ID))
	require.Equal(t, StatusCancelled, f.rowStatus(t, row.ID).Status)
	require.Equal(t, revisionID, *f.target(t).ActiveRevisionID, "the environment was never stopped")

	require.ErrorIs(t, f.controller.Cancel(ctx, runID), journal.ErrInvalidTransition,
		"a finished run cannot be cancelled again")
}

// A worker checks the run before it claims the row: a run that ended while
// the row waited (cancelled before the row was recorded, or by a path that
// only touched the journal) leaves the row cancelled, never run.
func TestProcessSkipsRowOfFinishedRun(t *testing.T) {
	f := newServiceFixture(t)
	ctx := context.Background()
	revisionID := f.activate(t, databaseDefinition())
	runID, row := f.beginRow(t, KindRestore, &revisionID)
	require.NoError(t, f.journal.FinishRun(ctx, runID, journal.RunCancelled))

	require.NoError(t, f.controller.process(ctx, row.ID))
	require.Equal(t, StatusCancelled, f.rowStatus(t, row.ID).Status)
	tree, err := f.journal.RunTree(ctx, runID)
	require.NoError(t, err)
	require.Empty(t, tree.Steps, "no step of the restore ran")
	require.Equal(t, revisionID, *f.target(t).ActiveRevisionID)
}

// blockingStore is a target that never answers until its caller gives up.
type blockingStore struct {
	*memoryStore
	entered chan struct{}
}

func (b *blockingStore) Reachable(ctx context.Context) error {
	close(b.entered)
	<-ctx.Done()
	return ctx.Err()
}

// A cancel interrupts a running backup: the work's context ends, the row
// and the run conclude cancelled, and the environment's run slot frees.
func TestCancelInterruptsRunningBackup(t *testing.T) {
	f := newServiceFixture(t)
	ctx := context.Background()
	f.configureTarget(t)
	f.activate(t, databaseDefinition())
	target := &blockingStore{memoryStore: newMemoryStore(), entered: make(chan struct{})}
	f.controller.openStore = func(s3Location) (objectStore, error) { return target, nil }

	result, err := f.controller.CreateBackup(ctx, BackupInput{EnvironmentID: f.environmentID, Actor: "tester"})
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- f.controller.process(ctx, result.BackupID) }()
	select {
	case <-target.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the backup never reached its target check")
	}
	require.False(t, f.slotFree(t))

	require.NoError(t, f.controller.Cancel(ctx, result.RunID))
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("the cancel did not interrupt the backup")
	}
	require.Equal(t, string(journal.RunCancelled), f.runStatus(t, result.RunID))
	row := f.rowStatus(t, result.BackupID)
	require.Equal(t, StatusCancelled, row.Status)
	require.NotNil(t, row.FinishedAt)
	require.True(t, f.slotFree(t))
}

// cancellingRevisions cancels the run in the journal while the restore
// loads the revision it resumes, after its snapshot step finished.
type cancellingRevisions struct {
	revisionLoader
	cancel func()
}

func (r cancellingRevisions) GetRevision(ctx context.Context, id uuid.UUID) (*revision.Revision, error) {
	r.cancel()
	return r.revisionLoader.GetRevision(ctx, id)
}

// cancellingStore cancels the run in the journal while the restore reads
// its snapshot manifest, inside a step.
type cancellingStore struct {
	*memoryStore
	cancel func()
}

func (s *cancellingStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	s.cancel()
	return s.memoryStore.Get(ctx, key)
}

// restoreFixture records a restore of a written snapshot into the fixture
// environment.
func (f *serviceFixture) restoreFixture(t *testing.T) (uuid.UUID, uuid.UUID, store.Backup, *memoryStore) {
	t.Helper()
	f.configureTarget(t)
	revisionID := f.activate(t, databaseDefinition())
	target := newMemoryStore()
	writeSnapshot(t, target, snapshotManifest("snapshot", "production", TriggerManual, time.Now()))
	runID, row := f.beginRow(t, KindRestore, &revisionID)
	require.NoError(t, f.st.SetBackupSnapshotKey(context.Background(), store.SetBackupSnapshotKeyParams{
		ID: row.ID, SnapshotKey: manifestKey("", "demo", "production", "snapshot"),
	}))
	f.controller.openStore = func(s3Location) (objectStore, error) { return target, nil }
	return revisionID, runID, row, target
}

// A cancel that lands after the restore loaded its snapshot, right before
// it would stop the environment, leaves the environment running.
func TestCancelBeforeStopLeavesEnvironmentRunning(t *testing.T) {
	f := newServiceFixture(t)
	ctx := context.Background()
	revisionID, runID, row, _ := f.restoreFixture(t)
	f.controller.revisions = cancellingRevisions{revisionLoader: f.controller.revisions, cancel: func() {
		require.NoError(t, f.journal.FinishRun(ctx, runID, journal.RunCancelled))
	}}

	require.NoError(t, f.controller.process(ctx, row.ID))
	require.Equal(t, StatusCancelled, f.rowStatus(t, row.ID).Status)
	require.Equal(t, string(journal.RunCancelled), f.runStatus(t, runID))
	require.Equal(t, revisionID, *f.target(t).ActiveRevisionID, "the environment was never stopped")
}

// A cancel that reaches only the journal while a step runs fails that
// step's own completion; the row still concludes cancelled, like its run.
func TestCancelDuringStepConcludesCancelled(t *testing.T) {
	f := newServiceFixture(t)
	ctx := context.Background()
	revisionID, runID, row, target := f.restoreFixture(t)
	f.controller.openStore = func(s3Location) (objectStore, error) {
		return &cancellingStore{memoryStore: target, cancel: func() {
			require.NoError(t, f.journal.FinishRun(ctx, runID, journal.RunCancelled))
		}}, nil
	}

	require.NoError(t, f.controller.process(ctx, row.ID))
	require.Equal(t, StatusCancelled, f.rowStatus(t, row.ID).Status)
	require.Equal(t, string(journal.RunCancelled), f.runStatus(t, runID))
	require.Equal(t, revisionID, *f.target(t).ActiveRevisionID)
}

// A row whose redactor cannot be built fails together with its run, which
// would otherwise hold the environment's run slot.
func TestRedactorFailureFinishesRun(t *testing.T) {
	f := newServiceFixture(t)
	ctx := context.Background()
	revisionID := f.activate(t, databaseDefinition())
	// Values written under another key cannot be decrypted.
	other, err := valuestore.New(f.st, strings.Repeat("o", 32))
	require.NoError(t, err)
	candidate, err := other.Stage(ctx, f.environmentID, map[string]string{"TOKEN": "value"})
	require.NoError(t, err)
	require.NoError(t, other.PromoteTx(ctx, f.st.Queries, f.environmentID, candidate.ID))
	runID, row := f.beginRow(t, KindBackup, &revisionID)

	require.NoError(t, f.controller.process(ctx, row.ID))
	failed := f.rowStatus(t, row.ID)
	require.Equal(t, StatusFailed, failed.Status)
	require.Contains(t, *failed.Error, "build redactor")
	require.Equal(t, string(journal.RunFailed), f.runStatus(t, runID))
	require.True(t, f.slotFree(t))
}

// The accept path's cleanup lands even when the request that began the
// run is already gone.
func TestAbandonOutlivesTheRequest(t *testing.T) {
	f := newServiceFixture(t)
	runID, row := f.beginRow(t, KindRestore, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	f.controller.abandon(ctx, runID, &row.ID, "recording the snapshot key failed")
	require.Equal(t, string(journal.RunFailed), f.runStatus(t, runID))
	require.Equal(t, StatusFailed, f.rowStatus(t, row.ID).Status)
	require.True(t, f.slotFree(t))
}

// The sweep fails a run whose row already finished, and one that never got
// a row once the accept path had time to record it; a run whose row is
// still in flight, or that was begun a moment ago, stays.
func TestSweepFailsStrandedRuns(t *testing.T) {
	f := newServiceFixture(t)
	ctx := context.Background()
	age := func(runID uuid.UUID) {
		_, err := f.st.Pool.Exec(ctx, "UPDATE runs SET created_at = now() - interval '2 minutes' WHERE id = $1", runID)
		require.NoError(t, err)
	}
	finish := func(runID uuid.UUID) {
		require.NoError(t, f.journal.FinishRun(ctx, runID, journal.RunCancelled))
	}

	finishedRow, row := f.beginRow(t, KindBackup, nil)
	_, err := f.st.SetBackupStatus(ctx, store.SetBackupStatusParams{
		ID: row.ID, ToStatus: StatusRunning, FromStatus: StatusPending,
	})
	require.NoError(t, err)
	require.NoError(t, f.controller.failRow(ctx, row.ID, "failed"))
	age(finishedRow)
	require.NoError(t, f.controller.sweep(ctx))
	require.Equal(t, string(journal.RunFailed), f.runStatus(t, finishedRow))

	inFlight, _ := f.beginRow(t, KindBackup, nil)
	age(inFlight)
	require.NoError(t, f.controller.sweep(ctx))
	require.Equal(t, string(journal.RunRunning), f.runStatus(t, inFlight), "a pending row is in flight")
	finish(inFlight)

	rowless, err := f.journal.BeginRun(ctx, journal.RunInput{
		Kind: KindRestore, ProjectID: f.projectID, EnvironmentID: f.environmentID, Actor: "tester",
	})
	require.NoError(t, err)
	require.NoError(t, f.controller.sweep(ctx))
	require.Equal(t, string(journal.RunRunning), f.runStatus(t, rowless.ID), "the accept path may still record its row")
	age(rowless.ID)
	require.NoError(t, f.controller.sweep(ctx))
	require.Equal(t, string(journal.RunFailed), f.runStatus(t, rowless.ID))
	require.True(t, f.slotFree(t))
}

func (f *serviceFixture) target(t *testing.T) store.EnvironmentTarget {
	t.Helper()
	target, err := f.st.GetEnvironmentTarget(context.Background(), f.environmentID)
	require.NoError(t, err)
	return target
}
