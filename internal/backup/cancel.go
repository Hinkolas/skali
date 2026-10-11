package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/store"
)

// StatusCancelled marks a row a cancel reached: before it started, so it
// never ran, or while it ran, so its work was interrupted.
const StatusCancelled = "cancelled"

const (
	// concludeTimeout bounds the writes that finish a run and its row on a
	// context of their own.
	concludeTimeout = 30 * time.Second
	// sweepInterval is how often stranded runs are looked for.
	sweepInterval = time.Minute
	// strandedRunGrace is how old a run must be before a missing row
	// strands it: the accept path begins the run a moment before it
	// records the row.
	strandedRunGrace = time.Minute
)

// detached is a context for writes that must land although the work's own
// context ended: a cancel interrupted the work, or the request or scheduler
// tick that accepted it is done. A run left running holds its environment's
// one run slot and blocks every deploy.
func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), concludeTimeout)
}

// Cancel stops a backup or restore run. The run finishes cancelled first:
// that is what the caller sees, and it frees the environment's run slot. A
// row still waiting then never starts; a running row has its context
// cancelled, which interrupts copies and Job waits, and its worker removes
// the Jobs and concludes the row. A run that already finished answers the
// journal's ErrInvalidTransition.
func (c *Controller) Cancel(ctx context.Context, runID uuid.UUID) error {
	ctx, cancel := detached(ctx)
	defer cancel()
	if err := c.deps.Journal.FinishRun(ctx, runID, journal.RunCancelled); err != nil {
		return err
	}
	row, err := c.deps.Store.GetBackupByRun(ctx, &runID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Accepted a moment ago, or never recorded: no worker will start
		// what the run no longer allows.
		return nil
	}
	if err != nil {
		return fmt.Errorf("backup: get row of run: %w", err)
	}
	if row.Status == StatusPending {
		cancelled, err := c.setCancelled(ctx, row.ID)
		if err != nil || cancelled {
			return err
		}
	}
	// A worker tracks its row before it reads the run, so a row it claimed
	// after the read above is found here.
	c.mu.Lock()
	stop := c.running[runID]
	c.mu.Unlock()
	if stop != nil {
		stop(errCancelled)
	}
	return nil
}

// setCancelled moves a pending row to cancelled, reporting whether it was
// still pending.
func (c *Controller) setCancelled(ctx context.Context, id uuid.UUID) (bool, error) {
	message := "cancelled"
	moved, err := c.deps.Store.SetBackupStatus(ctx, store.SetBackupStatusParams{
		ID: id, ToStatus: StatusCancelled, FromStatus: StatusPending, Error: &message,
	})
	if err != nil {
		return false, fmt.Errorf("backup: cancel row: %w", err)
	}
	return moved > 0, nil
}

// track registers a row's cancel under its run until the returned func
// runs.
func (c *Controller) track(runID uuid.UUID, stop context.CancelCauseFunc) func() {
	c.mu.Lock()
	c.running[runID] = stop
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		delete(c.running, runID)
		c.mu.Unlock()
	}
}

// conclude drives the run and then the row terminal and removes the Jobs
// and Secrets the operation created, which stops a Job a cancel left
// running. The run finishes first, so a row never ends before its run
// does (the sweep relies on that).
func (c *Controller) conclude(ctx context.Context, row *store.Backup, scope *journal.Scope, err error) error {
	ctx, cancel := detached(ctx)
	defer cancel()
	// The run's failure reason and the row's error are the same text by
	// construction: whoever reads either sees the same summary.
	runStatus, rowStatus, message := journal.RunSucceeded, StatusSucceeded, ""
	switch {
	case errors.Is(err, errCancelled):
		runStatus, rowStatus, message = journal.RunCancelled, StatusCancelled, "cancelled"
	case err != nil:
		runStatus, rowStatus, message = journal.RunFailed, StatusFailed, err.Error()
	}
	scope.Finish(ctx, runStatus, message)
	c.cleanupJobs(ctx, row)
	var rowError *string
	if err != nil {
		rowError = &message
	}
	if _, err := c.deps.Store.SetBackupStatus(ctx, store.SetBackupStatusParams{
		ID: row.ID, ToStatus: rowStatus, FromStatus: StatusRunning, Error: rowError,
	}); err != nil {
		return fmt.Errorf("backup: finish row: %w", err)
	}
	if row.Kind == KindRestore {
		// A restore that stopped the environment shows in its status
		// until it completes; the row's outcome is part of that.
		c.statusChanged(row.EnvironmentID)
	}
	return nil
}

func (c *Controller) statusChanged(environmentID uuid.UUID) {
	if c.deps.StatusChanged != nil {
		c.deps.StatusChanged(environmentID)
	}
}

// abandon fails a run the accept path began but could not hand to a
// worker, and the row it recorded, if any.
func (c *Controller) abandon(ctx context.Context, runID uuid.UUID, rowID *uuid.UUID, reason string) {
	ctx, cancel := detached(ctx)
	defer cancel()
	if err := c.deps.Journal.FailRun(ctx, runID, nil, reason); err != nil &&
		!errors.Is(err, journal.ErrInvalidTransition) {
		slog.WarnContext(ctx, "backup: fail abandoned run", "run", runID, "err", err)
	}
	if rowID == nil {
		return
	}
	if _, err := c.deps.Store.SetBackupStatus(ctx, store.SetBackupStatusParams{
		ID: *rowID, ToStatus: StatusFailed, FromStatus: StatusPending, Error: &reason,
	}); err != nil {
		slog.WarnContext(ctx, "backup: fail abandoned row", "backup", *rowID, "err", err)
	}
}

// sweep fails every backup and restore run whose row already finished or
// was never recorded. Such a run can only come from a failure between the
// two writes, and it would hold the environment's run slot until someone
// cancelled it by hand.
func (c *Controller) sweep(ctx context.Context) error {
	runs, err := c.deps.Store.ListStrandedBackupRuns(ctx, time.Now().Add(-strandedRunGrace))
	if err != nil {
		return fmt.Errorf("backup: list stranded runs: %w", err)
	}
	for _, run := range runs {
		reason := run.Kind + " ended without finishing its run"
		if err := c.deps.Journal.FailRun(ctx, run.ID, nil, reason); err != nil &&
			!errors.Is(err, journal.ErrInvalidTransition) {
			slog.WarnContext(ctx, "backup: fail stranded run", "run", run.ID, "err", err)
			continue
		}
		slog.InfoContext(ctx, "backup: failed stranded run", "run", run.ID, "kind", run.Kind)
	}
	return nil
}
