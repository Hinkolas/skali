package reconcile

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/deploy"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/redact"
	"github.com/Hinkolas/skali/internal/store"
)

// warn logs a journal failure unless it is plain shutdown noise, or a
// write refused because another writer (a cancel) finished the run.
func warn(message string, err error, args ...any) {
	if errors.Is(err, context.Canceled) || errors.Is(err, journal.ErrRunFinished) {
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
	journal *journal.Service
	// newRedactor builds the redactor on the first journal write, so a pass
	// that journals nothing never reads the values behind it; redactor
	// holds it once built.
	newRedactor func(context.Context) *redact.Redactor
	redactor    *redact.Redactor

	environmentID uuid.UUID
	projectID     uuid.UUID

	run        *attachedRun
	created    bool
	parent     *uuid.UUID // rollout parent step of an adopted deployment run
	ensureKind string     // kind of a lazily created run; "reconcile" if empty

	// steps is the run's steps as the pass knows them: read when it adopts
	// the run, empty for a run it created, and kept current by its own
	// records. A step known terminal is never written again, and a waiting
	// step known to hold the same line is not written at all.
	steps map[string]journal.StepState
}

// attachedRun is the run a pass journals under.
type attachedRun struct {
	ID   uuid.UUID
	Kind string
}

// adoptableRun is the environment's running run that attachRun would
// adopt, when there is one. It reads nothing, so the pre-lock probe phase
// can ask it for the probe cadence without touching the journal.
func adoptableRun(state store.GetEnvironmentPassRow) (*attachedRun, bool) {
	if state.RunID == nil || state.RunKind == nil {
		return nil, false
	}
	switch *state.RunKind {
	case "deployment", "rollback", "restart", "teardown", "reconcile":
	default:
		// The kernel adopts only runs whose lifecycle it owns. A backup or
		// restore run is driven by the backup controller; adopting it would
		// let a converged pass's activate() or the teardown path finish it
		// mid-flight. The pass still reconciles, and its lazy run loses the
		// environment's running-run race in ensure, journaling nothing.
		return nil, false
	}
	if state.DeploymentStatus != nil && *state.DeploymentStatus == string(deploy.DeploymentPreparing) {
		// A deployment run in its artifact window still belongs to the build
		// client: rollout journaling and the rollout deadline begin at
		// promote. Adopting earlier lets a stale unhealthy target fail a run
		// whose deployment is still preparing.
		return nil, false
	}
	return &attachedRun{ID: *state.RunID, Kind: *state.RunKind}, true
}

// attachRun adopts the environment's running run when one exists.
// newRedactor builds the redactor for its journal writes, on first use.
func (k *Kernel) attachRun(ctx context.Context, state store.GetEnvironmentPassRow,
	newRedactor func(context.Context) *redact.Redactor) *runAttachment {
	attachment := &runAttachment{
		journal:       k.deps.Journal,
		newRedactor:   newRedactor,
		environmentID: state.Environment.ID,
		projectID:     state.Environment.ProjectID,
	}
	run, ok := adoptableRun(state)
	if !ok {
		return attachment
	}
	attachment.run = run
	steps, err := attachment.journal.StepStates(ctx, run.ID)
	if err != nil {
		// Without the view every record goes to the journal, which
		// decides; only a wait an earlier pass left open stays open.
		warn("read run steps", err, "run", run.ID)
		steps = map[string]journal.StepState{}
	}
	attachment.steps = steps
	if rolloutRun(run.Kind) {
		parent, ok := steps["rollout"]
		if !ok {
			step, err := attachment.journal.EnsureStep(ctx, run.ID, nil, "rollout", "Roll out revision")
			if err != nil {
				warn("ensure rollout step", err, "run", run.ID)
				return attachment
			}
			parent = journal.StepState{ID: step.ID, Status: journal.StepStatus(step.Status)}
		}
		if parent.Status == journal.StepPending {
			if err := attachment.journal.SetStepStatus(ctx, parent.ID, journal.StepRunning); err != nil {
				warn("start rollout step", err, "run", run.ID)
			}
			parent.Status = journal.StepRunning
		}
		steps["rollout"] = parent
		attachment.parent = &parent.ID
	}
	return attachment
}

func (a *runAttachment) active() bool  { return a.run != nil }
func (a *runAttachment) adopted() bool { return a.run != nil && !a.created }

// redactorFor returns the redactor for a journal write, building it first.
func (a *runAttachment) redactorFor(ctx context.Context) *redact.Redactor {
	if a.redactor == nil && a.newRedactor != nil {
		a.redactor = a.newRedactor(ctx)
	}
	return a.redactor
}

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
	run, err := a.journal.BeginRun(ctx, journal.RunInput{
		Kind:          kind,
		ProjectID:     a.projectID,
		EnvironmentID: a.environmentID,
		Actor:         actorReconcile,
	})
	if err != nil {
		// With ErrRunConflict the environment already has a running run
		// this pass did not adopt (a deployment still inside its artifact
		// window, or one that started after attachRun read). The pass
		// journals nothing; the next pass adopts the run that won.
		warn("begin reconcile run", err, "environment", a.environmentID)
		return
	}
	a.run = &attachedRun{ID: run.ID, Kind: run.Kind}
	a.created = true
	a.steps = map[string]journal.StepState{}
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
	if journal.Steps.Terminal(a.steps[key].Status) {
		return
	}
	level := "info"
	if status == journal.StepFailed {
		level = "error"
	}
	entries := make([]journal.LogEntry, 0, len(logs))
	for _, line := range logs {
		entries = append(entries, journal.LogEntry{Level: level, Message: line, Fields: fields})
	}
	a.record(ctx, "complete step", journal.StepRecord{Key: key, Title: title, To: status, Entries: entries})
}

// record writes one step record of the attached run and keeps the view
// current. A step whose status refuses the record (terminal, or moved on by
// another writer) is left alone.
func (a *runAttachment) record(ctx context.Context, what string, record journal.StepRecord) {
	record.RunID, record.ParentID = a.run.ID, a.parent
	state, err := a.journal.RecordStep(ctx, record, a.redactorFor(ctx))
	if state.ID != uuid.Nil {
		if a.steps == nil {
			a.steps = map[string]journal.StepState{}
		}
		a.steps[record.Key] = state
	}
	if err != nil && !errors.Is(err, journal.ErrInvalidTransition) {
		warn(what, err, "key", record.Key)
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
	if step, known := a.steps[key]; known {
		switch step.Status {
		case journal.StepPending:
		case journal.StepWaiting:
			if step.Latest == reason {
				return // still waiting for the same reason
			}
		default:
			return // running or terminal: no repeat journaling
		}
	}
	a.record(ctx, "journal waiting step", journal.StepRecord{Key: key, Title: title, To: journal.StepWaiting,
		Entries: []journal.LogEntry{{Level: "info", Message: reason, Fields: fields}}, Dedupe: true})
}

// resolveWait closes a step an earlier pass left waiting, once the wait is
// over; a run that never waited on this key journals nothing, so the step
// only ever appears when there was something to wait for.
func (a *runAttachment) resolveWait(ctx context.Context, key, title string, logs []string) {
	if a.run == nil || a.steps[key].Status != journal.StepWaiting {
		return
	}
	a.completeStep(ctx, key, title, journal.StepSucceeded, logs)
}

// finish concludes the attached run. The rollout parent step of an adopted
// deployment run closes with the run. A failed run records reason as its
// one-line summary; other outcomes ignore it.
func (a *runAttachment) finish(ctx context.Context, status journal.RunStatus, reason string) {
	if a.run == nil {
		return
	}
	if a.parent != nil && status == journal.RunSucceeded {
		if err := a.journal.SetStepStatus(ctx, *a.parent, journal.StepSucceeded); err != nil &&
			!errors.Is(err, journal.ErrInvalidTransition) {
			warn("finish rollout step", err, "run", a.run.ID)
		}
	}
	var err error
	if status == journal.RunFailed {
		err = a.journal.FailRun(ctx, a.run.ID, a.redactorFor(ctx), reason)
	} else {
		err = a.journal.FinishRun(ctx, a.run.ID, status)
	}
	if err != nil && !errors.Is(err, journal.ErrInvalidTransition) {
		warn("finish run", err, "run", a.run.ID)
	}
	a.run = nil
	a.created = false
	a.parent = nil
	a.steps = nil
}
