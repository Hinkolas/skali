package backup

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/store"
)

// KindAccept is the journal run kind of accepting the data an incomplete
// restore left: the run records who lifted the hold, and when.
const KindAccept = "restore-accept"

// ErrNoIncompleteRestore: no incomplete restore holds the environment, or
// not the one the caller named (a later restore took the hold over).
var ErrNoIncompleteRestore = errors.New("backup: the named restore does not hold the environment")

// AcceptInput names the incomplete restore whose data the caller accepts:
// the one they were shown, so a hold that changed in between stays.
type AcceptInput struct {
	EnvironmentID uuid.UUID
	RestoreID     uuid.UUID
	Actor         string
}

// AcceptIncompleteRestore lifts the hold an incomplete restore keeps on
// the environment, accepting its data as it is: partially restored, or
// not restored at all where the restore never got to a component. The
// environment stays down; the next deployment brings it back on that data,
// and lifts the bucket fences with it. It runs as a journaled run of its
// own, which a restore or any other run in flight refuses.
func (c *Controller) AcceptIncompleteRestore(ctx context.Context, in AcceptInput) (uuid.UUID, error) {
	names, err := c.environmentNames(ctx, in.EnvironmentID)
	if err != nil {
		return uuid.Nil, err
	}
	held, err := c.heldBy(ctx, in.EnvironmentID, in.RestoreID)
	if err != nil {
		return uuid.Nil, err
	}
	run, err := c.deps.Journal.BeginRun(ctx, journal.RunInput{
		Kind:          KindAccept,
		ProjectID:     names.projectID,
		EnvironmentID: in.EnvironmentID,
		Actor:         in.Actor,
	})
	if errors.Is(err, journal.ErrRunConflict) {
		return uuid.Nil, ErrBackupInFlight
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("backup: begin run: %w", err)
	}
	// From here on a run exists that only this call concludes.
	ctx, cancel := detached(ctx)
	defer cancel()
	scope := journal.NewScope(c.deps.Journal, nil, run.ID)
	err = scope.Step(ctx, "accept", "Accept the incomplete restore's data", func(ctx context.Context, log *journal.StepLog) error {
		unlock, err := c.deps.Store.LockEnvironment(ctx, in.EnvironmentID)
		if err != nil {
			return err
		}
		defer unlock()
		cleared, err := c.deps.Store.ClearIncompleteRestore(ctx, store.ClearIncompleteRestoreParams{
			EnvironmentID: in.EnvironmentID, RestoreID: in.RestoreID,
		})
		if err != nil {
			return fmt.Errorf("clear the restore hold: %w", err)
		}
		if cleared == 0 {
			return ErrNoIncompleteRestore
		}
		log.Warn(ctx, fmt.Sprintf("restore %s of snapshot %s did not complete; its data is accepted as it is",
			held.ID, snapshotIDFromManifestKey(held.SnapshotKey)))
		if len(held.components) > 0 {
			log.Warn(ctx, "it had set out to rewrite "+strings.Join(held.components, ", ")+
				"; any of them may hold partial data")
		}
		log.Info(ctx, "the environment stays down until the next deployment")
		return nil
	})
	if err != nil {
		scope.Finish(ctx, journal.RunFailed, err.Error())
		return uuid.Nil, err
	}
	scope.Finish(ctx, journal.RunSucceeded, "")
	c.statusChanged(in.EnvironmentID)
	return run.ID, nil
}

// heldRestore is the restore row holding an environment, with the
// components the hold names.
type heldRestore struct {
	store.Backup
	components []string
}

// heldBy reads the restore holding the environment, refusing when it is
// not restoreID.
func (c *Controller) heldBy(ctx context.Context, environmentID, restoreID uuid.UUID) (*heldRestore, error) {
	target, err := c.deps.Store.GetEnvironmentTarget(ctx, environmentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrEnvironmentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("backup: get environment target: %w", err)
	}
	if target.IncompleteRestoreID == nil || *target.IncompleteRestoreID != restoreID {
		return nil, ErrNoIncompleteRestore
	}
	row, err := c.deps.Store.GetBackup(ctx, restoreID)
	if err != nil {
		return nil, fmt.Errorf("backup: get the incomplete restore: %w", err)
	}
	return &heldRestore{Backup: row, components: target.IncompleteRestoreComponents}, nil
}
