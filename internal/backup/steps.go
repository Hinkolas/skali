package backup

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/redact"
)

// runScope journals one backup or restore run. Unlike the reconcile
// kernel's best-effort attachment, the backup controller OWNS its runs:
// journal failures here fail the operation, because a backup whose
// explanation cannot be recorded should not silently continue.
type runScope struct {
	journal  *journal.Service
	redactor *redact.Redactor
	runID    uuid.UUID
}

// stepLog is the redacted writer handed to step bodies, plus progress.
type stepLog struct {
	writer *journal.Writer
	scope  *runScope
	stepID uuid.UUID
}

func (l *stepLog) Info(ctx context.Context, message string) {
	_ = l.writer.Info(ctx, message)
}

func (l *stepLog) Error(ctx context.Context, message string) {
	_ = l.writer.Error(ctx, message)
}

func (l *stepLog) Progress(ctx context.Context, current, total int64) {
	_ = l.scope.journal.SetStepProgress(ctx, l.stepID, current, total)
}

// step runs fn under a journal step with one attempt: ensure -> running ->
// attempt -> fn -> terminal. fn's error fails the attempt and step and is
// returned unchanged.
func (r *runScope) step(ctx context.Context, key, title string, fn func(ctx context.Context, log *stepLog) error) error {
	step, err := r.journal.EnsureStep(ctx, r.runID, nil, key, title)
	if err != nil {
		return fmt.Errorf("backup: ensure step %s: %w", key, err)
	}
	if journal.StepStatus(step.Status) == journal.StepPending {
		if err := r.journal.SetStepStatus(ctx, step.ID, journal.StepRunning); err != nil {
			return fmt.Errorf("backup: start step %s: %w", key, err)
		}
	}
	attempt, err := r.journal.StartAttempt(ctx, step.ID)
	if err != nil {
		return fmt.Errorf("backup: start attempt for %s: %w", key, err)
	}
	log := &stepLog{writer: r.journal.Writer(attempt.ID, r.redactor), scope: r, stepID: step.ID}
	if err := fn(ctx, log); err != nil {
		log.Error(ctx, err.Error())
		_ = r.journal.FinishAttempt(ctx, attempt.ID, journal.AttemptFailed)
		_ = r.journal.SetStepStatus(ctx, step.ID, journal.StepFailed)
		return err
	}
	if err := r.journal.FinishAttempt(ctx, attempt.ID, journal.AttemptSucceeded); err != nil {
		return fmt.Errorf("backup: finish attempt for %s: %w", key, err)
	}
	if err := r.journal.SetStepStatus(ctx, step.ID, journal.StepSucceeded); err != nil {
		return fmt.Errorf("backup: finish step %s: %w", key, err)
	}
	return nil
}

// skip records a step that will not run, with the reason in its title.
func (r *runScope) skip(ctx context.Context, key, title string) {
	step, err := r.journal.EnsureStep(ctx, r.runID, nil, key, title)
	if err != nil {
		return
	}
	_ = r.journal.SetStepStatus(ctx, step.ID, journal.StepSkipped)
}

// finish drives the run terminal, tolerating an already-terminal run.
func (r *runScope) finish(ctx context.Context, status journal.RunStatus) {
	if err := r.journal.FinishRun(ctx, r.runID, status); err != nil &&
		!errors.Is(err, journal.ErrInvalidTransition) {
		_ = err
	}
}

// cancelled reports whether the run was driven terminal from outside (a
// user cancel); callers check it between components and abort.
func (r *runScope) cancelled(ctx context.Context) bool {
	run, err := r.journal.Run(ctx, r.runID)
	if err != nil {
		return false
	}
	return journal.RunStatus(run.Status) != journal.RunRunning
}
