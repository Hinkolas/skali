package journal

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/redact"
)

// Scope journals one run a controller OWNS (a backup, a restore, a
// credential rotation). Unlike the reconcile kernel's best-effort
// attachment, journal failures here fail the operation, because an
// operation whose explanation cannot be recorded should not silently
// continue.
type Scope struct {
	journal  *Service
	redactor *redact.Redactor
	runID    uuid.UUID
}

// NewScope binds a run to the journal; a nil redactor passes log lines
// through unchanged (only for operations that never log secrets).
func NewScope(journal *Service, redactor *redact.Redactor, runID uuid.UUID) *Scope {
	return &Scope{journal: journal, redactor: redactor, runID: runID}
}

// RunID is the journaled run.
func (r *Scope) RunID() uuid.UUID { return r.runID }

// StepLog is the redacted writer handed to step bodies, plus progress.
type StepLog struct {
	writer *Writer
	scope  *Scope
	stepID uuid.UUID
}

func (l *StepLog) Info(ctx context.Context, message string) {
	_ = l.writer.Info(ctx, message)
}

func (l *StepLog) Warn(ctx context.Context, message string) {
	_ = l.writer.Warn(ctx, message)
}

func (l *StepLog) Error(ctx context.Context, message string) {
	_ = l.writer.Error(ctx, message)
}

func (l *StepLog) Progress(ctx context.Context, current, total int64) {
	_ = l.scope.journal.SetStepProgress(ctx, l.stepID, current, total)
}

// Step runs fn under a journal step with one attempt: ensure -> running ->
// attempt -> fn -> terminal. fn's error fails the attempt and step and is
// returned unchanged.
func (r *Scope) Step(ctx context.Context, key, title string, fn func(ctx context.Context, log *StepLog) error) error {
	step, err := r.journal.EnsureStep(ctx, r.runID, nil, key, title)
	if err != nil {
		return fmt.Errorf("journal: ensure step %s: %w", key, err)
	}
	if StepStatus(step.Status) == StepPending {
		if err := r.journal.SetStepStatus(ctx, step.ID, StepRunning); err != nil {
			return fmt.Errorf("journal: start step %s: %w", key, err)
		}
	}
	attempt, err := r.journal.StartAttempt(ctx, step.ID)
	if err != nil {
		return fmt.Errorf("journal: start attempt for %s: %w", key, err)
	}
	log := &StepLog{writer: r.journal.Writer(attempt.ID, r.redactor), scope: r, stepID: step.ID}
	if err := fn(ctx, log); err != nil {
		log.Error(ctx, err.Error())
		_ = r.journal.FinishAttempt(ctx, attempt.ID, AttemptFailed)
		_ = r.journal.SetStepStatus(ctx, step.ID, StepFailed)
		return err
	}
	if err := r.journal.FinishAttempt(ctx, attempt.ID, AttemptSucceeded); err != nil {
		return fmt.Errorf("journal: finish attempt for %s: %w", key, err)
	}
	if err := r.journal.SetStepStatus(ctx, step.ID, StepSucceeded); err != nil {
		return fmt.Errorf("journal: finish step %s: %w", key, err)
	}
	return nil
}

// Skip records a step that will not run, with the reason in its title.
func (r *Scope) Skip(ctx context.Context, key, title string) {
	step, err := r.journal.EnsureStep(ctx, r.runID, nil, key, title)
	if err != nil {
		return
	}
	_ = r.journal.SetStepStatus(ctx, step.ID, StepSkipped)
}

// Finish drives the run terminal, tolerating an already-terminal run. A
// failed run records reason, redacted, as its one-line summary.
func (r *Scope) Finish(ctx context.Context, status RunStatus, reason string) {
	var err error
	if status == RunFailed {
		err = r.journal.FailRun(ctx, r.runID, r.redactor, reason)
	} else {
		err = r.journal.FinishRun(ctx, r.runID, status)
	}
	if err != nil && !errors.Is(err, ErrInvalidTransition) {
		_ = err
	}
}

// Cancelled reports whether the run was driven terminal from outside (a
// user cancel); callers check it between units of work and abort.
func (r *Scope) Cancelled(ctx context.Context) bool {
	run, err := r.journal.Run(ctx, r.runID)
	if err != nil {
		return false
	}
	return RunStatus(run.Status) != RunRunning
}
