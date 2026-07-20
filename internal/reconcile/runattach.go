package reconcile

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/redact"
	"github.com/Hinkolas/skali/internal/store"
)

// warn logs a journal failure unless it is plain shutdown noise.
func warn(message string, err error, args ...any) {
	if errors.Is(err, context.Canceled) {
		return
	}
	slog.Warn(message, append(args, "error", err)...)
}

const actorReconcile = "system:reconcile"

// runAttachment is the pass's explanatory journal handle. It adopts the
// environment's running deployment run when one is in flight (reattaching
// through deterministic step keys after a restart) and lazily creates a run
// of kind "reconcile" on the first material work otherwise. Every journal
// write is best-effort: a journal failure is logged and never blocks
// reconciliation, because runs explain and never drive.
type runAttachment struct {
	journal  *journal.Service
	redactor *redact.Redactor

	environmentID uuid.UUID
	projectID     uuid.UUID

	run     *store.Run
	created bool
	parent  *uuid.UUID // rollout parent step of an adopted deployment run
}

// attachRun adopts the environment's running run when one exists.
func (k *Kernel) attachRun(ctx context.Context, environmentID, projectID uuid.UUID, redactor *redact.Redactor) *runAttachment {
	attachment := &runAttachment{
		journal:       k.deps.Journal,
		redactor:      redactor,
		environmentID: environmentID,
		projectID:     projectID,
	}
	run, err := k.deps.Store.GetRunningRunByEnvironment(ctx, &environmentID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			warn("adopt run", err, "environment", environmentID)
		}
		return attachment
	}
	attachment.run = &run
	if run.Kind == "deployment" {
		step, err := attachment.journal.EnsureStep(ctx, run.ID, nil, "rollout", "Roll out revision")
		if err != nil {
			warn("ensure rollout step", err, "run", run.ID)
			return attachment
		}
		if journal.StepStatus(step.Status) == journal.StepPending {
			if err := attachment.journal.SetStepStatus(ctx, step.ID, journal.StepRunning); err != nil {
				warn("start rollout step", err, "run", run.ID)
			}
		}
		attachment.parent = &step.ID
	}
	return attachment
}

func (a *runAttachment) active() bool  { return a.run != nil }
func (a *runAttachment) adopted() bool { return a.run != nil && !a.created }

// ensure creates the reconcile-kind run on first material work.
func (a *runAttachment) ensure(ctx context.Context) {
	if a.run != nil {
		return
	}
	run, err := a.journal.CreateRun(ctx, journal.RunInput{
		Kind:          "reconcile",
		ProjectID:     a.projectID,
		EnvironmentID: a.environmentID,
		Actor:         actorReconcile,
	})
	if err != nil {
		warn("create reconcile run", err, "environment", a.environmentID)
		return
	}
	if err := a.journal.StartRun(ctx, run.ID); err != nil {
		warn("start reconcile run", err, "environment", a.environmentID)
		return
	}
	a.run = run
	a.created = true
}

// completeStep journals one already-performed piece of work as a step with
// a single finished attempt. A terminal step of the same key (an earlier
// pass in this run already did this) skips silently.
func (a *runAttachment) completeStep(ctx context.Context, key, title string, status journal.StepStatus, logs []string) {
	if a.run == nil {
		return
	}
	step, err := a.journal.EnsureStep(ctx, a.run.ID, a.parent, key, title)
	if err != nil {
		warn("ensure step", err, "key", key)
		return
	}
	current := journal.StepStatus(step.Status)
	if journal.Steps.Terminal(current) {
		return
	}
	if current == journal.StepPending || current == journal.StepWaiting {
		if err := a.journal.SetStepStatus(ctx, step.ID, journal.StepRunning); err != nil {
			warn("start step", err, "key", key)
			return
		}
	}
	attempt, err := a.journal.StartAttempt(ctx, step.ID)
	if err != nil {
		warn("start attempt", err, "key", key)
		return
	}
	writer := a.journal.Writer(attempt.ID, a.redactor)
	for _, line := range logs {
		if status == journal.StepFailed {
			_ = writer.Error(ctx, line)
		} else {
			_ = writer.Info(ctx, line)
		}
	}
	attemptStatus := journal.AttemptSucceeded
	if status == journal.StepFailed {
		attemptStatus = journal.AttemptFailed
	}
	if err := a.journal.FinishAttempt(ctx, attempt.ID, attemptStatus); err != nil {
		warn("finish attempt", err, "key", key)
	}
	if err := a.journal.SetStepStatus(ctx, step.ID, status); err != nil {
		warn("finish step", err, "key", key)
	}
}

// waitStep journals a visible waiting step: a dependency that is not ready
// names what it waits for instead of retrying opaquely (section 8.3). Only
// an in-flight run carries waiting steps; idle drift passes stay silent.
func (a *runAttachment) waitStep(ctx context.Context, key, title, reason string) {
	if a.run == nil {
		return
	}
	step, err := a.journal.EnsureStep(ctx, a.run.ID, a.parent, key, title)
	if err != nil {
		warn("ensure waiting step", err, "key", key)
		return
	}
	current := journal.StepStatus(step.Status)
	if current != journal.StepPending {
		return // already waiting, running, or terminal: no repeat journaling
	}
	if err := a.journal.SetStepStatus(ctx, step.ID, journal.StepWaiting); err != nil {
		warn("mark step waiting", err, "key", key)
		return
	}
	attempt, err := a.journal.StartAttempt(ctx, step.ID)
	if err != nil {
		return
	}
	writer := a.journal.Writer(attempt.ID, a.redactor)
	_ = writer.Info(ctx, reason)
	_ = a.journal.FinishAttempt(ctx, attempt.ID, journal.AttemptSucceeded)
}

// finish concludes the attached run. The rollout parent step of an adopted
// deployment run closes with the run.
func (a *runAttachment) finish(ctx context.Context, status journal.RunStatus) {
	if a.run == nil {
		return
	}
	if a.parent != nil && status == journal.RunSucceeded {
		if err := a.journal.SetStepStatus(ctx, *a.parent, journal.StepSucceeded); err != nil &&
			!errors.Is(err, journal.ErrInvalidTransition) {
			warn("finish rollout step", err, "run", a.run.ID)
		}
	}
	if err := a.journal.FinishRun(ctx, a.run.ID, status); err != nil &&
		!errors.Is(err, journal.ErrInvalidTransition) {
		warn("finish run", err, "run", a.run.ID)
	}
	a.run = nil
	a.created = false
	a.parent = nil
}
