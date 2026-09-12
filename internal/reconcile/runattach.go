package reconcile

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/deploy"
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

// rolloutRun reports whether an adopted run of this kind owns the rollout:
// it carries the rollout parent step and the rollout deadline applies.
func rolloutRun(kind string) bool {
	return kind == "deployment" || kind == "rollback" || kind == "restart"
}

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

	run        *store.Run
	created    bool
	parent     *uuid.UUID // rollout parent step of an adopted deployment run
	ensureKind string     // kind of a lazily created run; "reconcile" if empty
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
	switch run.Kind {
	case "deployment", "rollback", "restart", "teardown", "reconcile":
	default:
		// The kernel adopts only runs whose lifecycle it owns. A backup or
		// restore run is driven by the backup controller; adopting it would
		// let a converged pass's activate() or the teardown path finish it
		// mid-flight. The pass still reconciles, and its lazy run loses the
		// StartRun race below, journaling nothing.
		return attachment
	}
	if run.Kind == "deployment" {
		// A deployment run in its artifact window still belongs to the build
		// client: rollout journaling and the rollout deadline begin at
		// promote. Adopting earlier lets a stale unhealthy target fail a run
		// whose deployment is still preparing.
		deployment, err := k.deps.Store.GetDeploymentByRunID(ctx, &run.ID)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				warn("look up adopted run deployment", err, "run", run.ID)
				return attachment
			}
		} else if deployment.Status == string(deploy.DeploymentPreparing) {
			return attachment
		}
	}
	attachment.run = &run
	if rolloutRun(run.Kind) {
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

// ensure creates the reconcile-kind run on first material work. A teardown
// pass overrides ensureKind so drift healing after the adopted run finished
// still journals under the truthful kind.
func (a *runAttachment) ensure(ctx context.Context) {
	if a.run != nil {
		return
	}
	kind := a.ensureKind
	if kind == "" {
		kind = "reconcile"
	}
	run, err := a.journal.CreateRun(ctx, journal.RunInput{
		Kind:          kind,
		ProjectID:     a.projectID,
		EnvironmentID: a.environmentID,
		Actor:         actorReconcile,
	})
	if err != nil {
		warn("create reconcile run", err, "environment", a.environmentID)
		return
	}
	if err := a.journal.StartRun(ctx, run.ID); err != nil {
		// The environment already has a running run this pass did not adopt
		// (a deployment still inside its artifact window, or one that started
		// after attachRun read). The created row can never start and nothing
		// would ever finish it, so it goes away and the pass journals
		// nothing; the next pass adopts the run that won.
		warn("start reconcile run", err, "environment", a.environmentID)
		if discardErr := a.journal.DiscardRun(ctx, run.ID); discardErr != nil {
			warn("discard unstarted reconcile run", discardErr, "run", run.ID)
		}
		return
	}
	a.run = run
	a.created = true
}

// completeStep journals one already-performed piece of work as a step with
// a single finished attempt. A terminal step of the same key (an earlier
// pass in this run already did this) skips silently.
func (a *runAttachment) completeStep(ctx context.Context, key, title string, status journal.StepStatus, logs []string) {
	a.completeStepFields(ctx, key, title, status, logs, nil)
}

// completeStepFields is completeStep with structured fields attached to
// every log line, for clients that render the outcome from data.
func (a *runAttachment) completeStepFields(ctx context.Context, key, title string, status journal.StepStatus, logs []string, fields map[string]any) {
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
		level := "info"
		if status == journal.StepFailed {
			level = "error"
		}
		_ = writer.Log(ctx, level, line, fields)
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
// names what it waits for instead of retrying opaquely, and a
// reason that changed across passes appends as a fresh line so the wait
// narrates its actual progress. Only an in-flight run carries waiting
// steps; idle drift passes stay silent.
func (a *runAttachment) waitStep(ctx context.Context, key, title, reason string) {
	a.waitStepFields(ctx, key, title, reason, nil)
}

// waitStepFields is waitStep with structured fields on the journaled
// reason; the reason text still decides whether anything new is written.
func (a *runAttachment) waitStepFields(ctx context.Context, key, title, reason string, fields map[string]any) {
	if a.run == nil {
		return
	}
	step, err := a.journal.EnsureStep(ctx, a.run.ID, a.parent, key, title)
	if err != nil {
		warn("ensure waiting step", err, "key", key)
		return
	}
	switch journal.StepStatus(step.Status) {
	case journal.StepPending:
		if err := a.journal.SetStepStatus(ctx, step.ID, journal.StepWaiting); err != nil {
			warn("mark step waiting", err, "key", key)
			return
		}
	case journal.StepWaiting:
		// Still waiting; only a changed reason journals below.
	default:
		return // running or terminal: no repeat journaling
	}
	if last, err := a.journal.LatestStepMessage(ctx, step.ID); err != nil || last == reason {
		return
	}
	attempt, err := a.journal.StartAttempt(ctx, step.ID)
	if err != nil {
		return
	}
	writer := a.journal.Writer(attempt.ID, a.redactor)
	_ = writer.Log(ctx, "info", reason, fields)
	_ = a.journal.FinishAttempt(ctx, attempt.ID, journal.AttemptSucceeded)
}

// resolveWait closes a step an earlier pass left waiting, once the wait is
// over; a run that never waited on this key journals nothing, so the step
// only ever appears when there was something to wait for.
func (a *runAttachment) resolveWait(ctx context.Context, key, title string, logs []string) {
	if a.run == nil {
		return
	}
	step, found, err := a.journal.FindStep(ctx, a.run.ID, key)
	if err != nil {
		warn("find waiting step", err, "key", key)
		return
	}
	if !found || journal.StepStatus(step.Status) != journal.StepWaiting {
		return
	}
	a.completeStep(ctx, key, title, journal.StepSucceeded, logs)
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
