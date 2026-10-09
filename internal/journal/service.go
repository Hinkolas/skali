package journal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/broadcast"
	"github.com/Hinkolas/skali/internal/lifecycle"
	"github.com/Hinkolas/skali/internal/redact"
	"github.com/Hinkolas/skali/internal/store"
)

var (
	ErrNotFound = errors.New("journal: not found")
	// ErrInvalidTransition: the requested status change is not permitted by
	// the run, step, or attempt lifecycle machine. This is the single write
	// guard; there is no other path into the journal tables.
	ErrInvalidTransition = errors.New("journal: invalid status transition")
	// ErrRunConflict: the environment already has a running run.
	ErrRunConflict = errors.New("journal: another run is already running for this environment")
	// ErrAttemptConflict: the step already has a running attempt.
	ErrAttemptConflict = errors.New("journal: an attempt is already running for this step")
	// ErrAttemptTerminal: log entries cannot be appended to a finished
	// attempt; the journal is append-only on live attempts.
	ErrAttemptTerminal = errors.New("journal: attempt is terminal")
)

// Service is the sole write path into the journal tables. Every status
// change consults the lifecycle machine: either under a row lock, or as one
// conditional update that applies only from the statuses the machine
// allows.
type Service struct {
	st         *store.Store
	executorID string
	broadcast  *broadcast.Broadcaster[LogEvent]
	// runWatch and envRunsWatch are the payload-free invalidation planes
	// behind the run-tree and run-list SSE streams (runstream.go).
	runWatch     *broadcast.Broadcaster[struct{}]
	envRunsWatch *broadcast.Broadcaster[struct{}]
}

// NewService binds the journal to this boot's executor identity (a fresh
// UUID per daemon start); attempts record it so recovery can tell which
// executors no longer exist.
func NewService(st *store.Store, executorID string) *Service {
	return &Service{
		st: st, executorID: executorID, broadcast: broadcast.New[LogEvent](256),
		runWatch: broadcast.New[struct{}](16), envRunsWatch: broadcast.New[struct{}](16),
	}
}

func (s *Service) ExecutorID() string { return s.executorID }

type RunInput struct {
	Kind          string
	ProjectID     uuid.UUID // uuid.Nil for installation-scoped runs
	EnvironmentID uuid.UUID // uuid.Nil for runs outside an environment
	Actor         string
	// BypassProtection marks a deployment that entered a promote-only
	// environment on an explicit admin bypass.
	BypassProtection bool
}

func (s *Service) CreateRun(ctx context.Context, in RunInput) (*store.Run, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("journal: generate id: %w", err)
	}
	params := store.CreateRunParams{ID: id, Kind: in.Kind, Actor: in.Actor, BypassProtection: in.BypassProtection}
	if in.ProjectID != uuid.Nil {
		params.ProjectID = &in.ProjectID
	}
	if in.EnvironmentID != uuid.Nil {
		params.EnvironmentID = &in.EnvironmentID
	}
	run, err := s.st.CreateRun(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("journal: create run: %w", err)
	}
	s.notifyEnvironment(params.EnvironmentID)
	return &run, nil
}

// StartRun moves pending -> running. The partial unique index turns a
// concurrent second start for the same environment into ErrRunConflict.
func (s *Service) StartRun(ctx context.Context, id uuid.UUID) error {
	var environmentID *uuid.UUID
	err := s.st.WithTx(ctx, func(q *store.Queries) error {
		run, err := q.GetRunForUpdate(ctx, id)
		if err != nil {
			return notFoundOr(err, "lock run")
		}
		if err := guard(Runs, RunStatus(run.Status), RunRunning); err != nil {
			return err
		}
		environmentID = run.EnvironmentID
		return q.MarkRunRunning(ctx, id)
	})
	if store.IsUniqueViolation(err) {
		return ErrRunConflict
	}
	if err != nil {
		return err
	}
	s.notifyRun(id)
	s.notifyEnvironment(environmentID)
	return nil
}

// DiscardRun removes a run that never started. A CreateRun whose StartRun
// lost the environment's running-run race would otherwise strand a pending
// row: nothing finishes it, the retention caps only reclaim terminal runs,
// and every reader counts it as in flight. The delete is guarded on the
// pending status, so a run that did start is never removed.
func (s *Service) DiscardRun(ctx context.Context, id uuid.UUID) error {
	run, err := s.st.GetRunByID(ctx, id)
	if err != nil {
		return notFoundOr(err, "get run")
	}
	rows, err := s.st.DeletePendingRun(ctx, id)
	if err != nil {
		return fmt.Errorf("journal: discard run: %w", err)
	}
	if rows == 0 {
		return nil // it started after all; its own writer owns it
	}
	s.notifyRun(id)
	s.notifyEnvironment(run.EnvironmentID)
	return nil
}

// MaxFailureBytes bounds a run's failure reason; longer reasons are cut with
// a truncation suffix. The reason is a one-line summary, never the detail.
const MaxFailureBytes = 512

// FinishRun moves the run to a terminal status and forces every non-terminal
// step and attempt terminal in the same transaction: running work adopts the
// run's outcome (failed or cancelled), unstarted steps are skipped. It then
// applies the retention caps. A run finished failed through this path
// records no reason; FailRun is the path that knows one.
func (s *Service) FinishRun(ctx context.Context, id uuid.UUID, to RunStatus) error {
	return s.finish(ctx, id, to, nil)
}

// FailRun finishes the run failed with a one-line reason, redacted through
// redactor (nil is fine) and bounded by MaxFailureBytes. The reason is the
// summary lists and closing lines show; it must be set by the code path that
// knows why the run failed, because a failure between steps leaves no step
// log to derive it from. An empty reason stores NULL.
func (s *Service) FailRun(ctx context.Context, id uuid.UUID, redactor *redact.Redactor, reason string) error {
	var failure *string
	if text := boundFailure(redactor.Redact(reason)); text != "" {
		failure = &text
	}
	return s.finish(ctx, id, RunFailed, failure)
}

// boundFailure trims a reason and cuts it at MaxFailureBytes.
func boundFailure(reason string) string {
	reason = strings.TrimSpace(reason)
	if len(reason) > MaxFailureBytes {
		reason = reason[:MaxFailureBytes-len(truncationSuffix)] + truncationSuffix
	}
	return reason
}

func (s *Service) finish(ctx context.Context, id uuid.UUID, to RunStatus, failure *string) error {
	if !Runs.Terminal(to) {
		return fmt.Errorf("%w: finish requires a terminal status, got %s", ErrInvalidTransition, to)
	}
	closeStatus := "cancelled"
	if to == RunFailed {
		closeStatus = "failed"
	} else {
		failure = nil // a reason only explains a failure
	}
	var environmentID *uuid.UUID
	err := s.st.WithTx(ctx, func(q *store.Queries) error {
		run, err := q.GetRunForUpdate(ctx, id)
		if err != nil {
			return notFoundOr(err, "lock run")
		}
		if err := guard(Runs, RunStatus(run.Status), to); err != nil {
			return err
		}
		environmentID = run.EnvironmentID
		if _, err := q.CloseRunningAttemptsForRun(ctx, store.CloseRunningAttemptsForRunParams{
			RunID: id, Status: closeStatus,
		}); err != nil {
			return fmt.Errorf("journal: close attempts: %w", err)
		}
		if _, err := q.CloseRunningSteps(ctx, store.CloseRunningStepsParams{
			RunID: id, Status: closeStatus,
		}); err != nil {
			return fmt.Errorf("journal: close steps: %w", err)
		}
		if _, err := q.SkipUnstartedSteps(ctx, id); err != nil {
			return fmt.Errorf("journal: skip steps: %w", err)
		}
		return q.MarkRunFinished(ctx, store.MarkRunFinishedParams{ID: id, Status: string(to), Failure: failure})
	})
	if err != nil {
		return err
	}
	err = s.prune(ctx, environmentID)
	s.notifyRun(id)
	s.notifyEnvironment(environmentID)
	return err
}

// EnsureStep is idempotent on (run_id, key): after a restart the controller
// reattaches to the same logical step instead of duplicating it.
func (s *Service) EnsureStep(ctx context.Context, runID uuid.UUID, parentID *uuid.UUID, key, title string) (*store.Step, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("journal: generate id: %w", err)
	}
	row, err := s.st.EnsureStep(ctx, store.EnsureStepParams{
		ID: id, RunID: runID, ParentID: parentID, Key: key, Title: title,
	})
	step := store.Step(row)
	if errors.Is(err, pgx.ErrNoRows) {
		// Another writer created it after the statement's snapshot.
		step, err = s.st.GetStepByRunAndKey(ctx, store.GetStepByRunAndKeyParams{RunID: runID, Key: key})
	}
	if err != nil {
		return nil, fmt.Errorf("journal: ensure step: %w", err)
	}
	s.notifyRun(runID)
	return &step, nil
}

// FindStep reads the step of a run by key without creating it; ok is false
// when the run never journaled that key.
func (s *Service) FindStep(ctx context.Context, runID uuid.UUID, key string) (*store.Step, bool, error) {
	step, err := s.st.GetStepByRunAndKey(ctx, store.GetStepByRunAndKeyParams{RunID: runID, Key: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("journal: read step: %w", err)
	}
	return &step, true, nil
}

func (s *Service) SetStepStatus(ctx context.Context, stepID uuid.UUID, to StepStatus) error {
	runID, err := s.st.SetStepStatus(ctx, store.SetStepStatusParams{
		ID: stepID, Status: string(to), FromStatuses: statuses(Steps.Sources(to)),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return s.refusedStep(ctx, stepID, to)
	}
	if err != nil {
		return fmt.Errorf("journal: set step status: %w", err)
	}
	s.notifyRun(runID)
	return nil
}

// LogEntry is one log line of a step journaled by CompleteStep.
type LogEntry struct {
	Level   string
	Message string
	Fields  map[string]any
}

// CompleteStep journals work that already happened: the step moves as if
// through running to succeeded or failed, with one finished attempt carrying
// entries, redacted and bounded like Append's. It is one guarded statement
// where opening the attempt, appending each line, and closing the attempt
// and step took a transaction each. A step whose status does not allow the
// change is refused with ErrInvalidTransition, and one with a running
// attempt with ErrAttemptConflict; neither writes anything.
func (s *Service) CompleteStep(ctx context.Context, step *store.Step, redactor *redact.Redactor, to StepStatus, entries []LogEntry) error {
	attemptStatus := AttemptSucceeded
	switch to {
	case StepSucceeded:
	case StepFailed:
		attemptStatus = AttemptFailed
	default:
		return fmt.Errorf("%w: a completed step succeeds or fails, got %s", ErrInvalidTransition, to)
	}
	attemptID, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("journal: generate id: %w", err)
	}
	type line struct {
		ID      uuid.UUID       `json:"id"`
		Level   string          `json:"level"`
		Message string          `json:"message"`
		Fields  json.RawMessage `json:"fields"`
	}
	lines := make([]line, 0, len(entries))
	for _, entry := range entries {
		if !validLevel(entry.Level) {
			return fmt.Errorf("journal: invalid log level %q", entry.Level)
		}
		fields, err := encodeFields(redactor, entry.Fields)
		if err != nil {
			return err
		}
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("journal: generate id: %w", err)
		}
		lines = append(lines, line{ID: id, Level: entry.Level, Message: boundEntry(redactor.Redact(entry.Message)), Fields: fields})
	}
	if len(lines) > MaxEntriesPerAttempt {
		lines = lines[:MaxEntriesPerAttempt]
		lines[MaxEntriesPerAttempt-1] = line{ID: lines[MaxEntriesPerAttempt-1].ID, Level: "warn",
			Message: "log truncated: entry cap reached", Fields: json.RawMessage("{}")}
	}
	encoded, err := json.Marshal(lines)
	if err != nil {
		return fmt.Errorf("journal: encode entries: %w", err)
	}
	from := append(Steps.Sources(StepRunning), StepRunning)
	row, err := s.st.CompleteStep(ctx, store.CompleteStepParams{
		ID: step.ID, Status: string(to), FromStatuses: statuses(from),
		AttemptID: attemptID, AttemptStatus: string(attemptStatus), ExecutorID: s.executorID,
		Entries: encoded,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		current, err := s.st.GetStepByID(ctx, step.ID)
		if err != nil {
			return notFoundOr(err, "get step")
		}
		if slices.Contains(from, StepStatus(current.Status)) {
			return ErrAttemptConflict
		}
		return fmt.Errorf("%w: %v -> %v", ErrInvalidTransition, current.Status, to)
	}
	if err != nil {
		return fmt.Errorf("journal: complete step: %w", err)
	}
	for i, line := range lines {
		s.broadcast.Publish(step.ID, LogEvent{
			StepID:        step.ID,
			AttemptNumber: row.Number,
			Seq:           int64(i + 1),
			TS:            row.LoggedAt,
			Level:         line.Level,
			Message:       line.Message,
			Fields:        line.Fields,
		})
	}
	s.notifyRun(step.RunID)
	return nil
}

// refusedStep explains a guarded step update that changed nothing: the
// step is gone, or its status does not allow the change.
func (s *Service) refusedStep(ctx context.Context, stepID uuid.UUID, to StepStatus) error {
	step, err := s.st.GetStepByID(ctx, stepID)
	if err != nil {
		return notFoundOr(err, "get step")
	}
	return fmt.Errorf("%w: %v -> %v", ErrInvalidTransition, step.Status, to)
}

func (s *Service) SetStepProgress(ctx context.Context, stepID uuid.UUID, current, total int64) error {
	if err := s.st.SetStepProgress(ctx, store.SetStepProgressParams{
		ID: stepID, ProgressCurrent: &current, ProgressTotal: &total,
	}); err != nil {
		return fmt.Errorf("journal: set progress: %w", err)
	}
	s.notifyRunOfStep(ctx, stepID)
	return nil
}

// LatestStepMessage returns a step's most recent log line across attempts,
// empty when none exist; waiting steps use it to append a fresh reason only
// when it changed.
func (s *Service) LatestStepMessage(ctx context.Context, stepID uuid.UUID) (string, error) {
	message, err := s.st.LatestStepLog(ctx, stepID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("journal: latest step log: %w", err)
	}
	return message, nil
}

// StartAttempt opens the next attempt of a running step. The partial unique
// index rejects a second running attempt; retries stay inside one step.
func (s *Service) StartAttempt(ctx context.Context, stepID uuid.UUID) (*store.Attempt, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("journal: generate id: %w", err)
	}
	attempt, err := s.st.CreateAttempt(ctx, store.CreateAttemptParams{
		ID: id, StepID: stepID, ExecutorID: s.executorID,
	})
	if err != nil {
		if store.IsUniqueViolation(err) {
			return nil, ErrAttemptConflict
		}
		return nil, fmt.Errorf("journal: create attempt: %w", err)
	}
	s.notifyRunOfStep(ctx, stepID)
	return &attempt, nil
}

func (s *Service) FinishAttempt(ctx context.Context, attemptID uuid.UUID, to AttemptStatus) error {
	runID, err := s.st.FinishAttempt(ctx, store.FinishAttemptParams{
		ID: attemptID, Status: string(to), FromStatuses: statuses(Attempts.Sources(to)),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		attempt, err := s.st.GetAttemptByID(ctx, attemptID)
		if err != nil {
			return notFoundOr(err, "get attempt")
		}
		return fmt.Errorf("%w: %v -> %v", ErrInvalidTransition, attempt.Status, to)
	}
	if err != nil {
		return fmt.Errorf("journal: finish attempt: %w", err)
	}
	s.notifyRun(runID)
	return nil
}

// Run reads one run row; cancellation and run-scoped authorization need
// the kind, environment, actor, and status without the full tree.
func (s *Service) Run(ctx context.Context, id uuid.UUID) (*store.Run, error) {
	row, err := s.st.GetRunByID(ctx, id)
	if err != nil {
		return nil, notFoundOr(err, "get run")
	}
	return &row, nil
}

func (s *Service) ListRuns(ctx context.Context, environmentID uuid.UUID) ([]store.Run, error) {
	runs, err := s.st.ListRunsByEnvironment(ctx, &environmentID)
	if err != nil {
		return nil, fmt.Errorf("journal: list runs: %w", err)
	}
	return runs, nil
}

// DeferredRoutes counts, per run, the TLS checkpoints that ended skipped
// because the route's domain did not reach this installation yet; runs
// without one are absent from the map. Aggregate views mark such runs,
// whose status stays succeeded, as having ended with warnings.
func (s *Service) DeferredRoutes(ctx context.Context, runIDs []uuid.UUID) (map[uuid.UUID]int64, error) {
	if len(runIDs) == 0 {
		return nil, nil
	}
	rows, err := s.st.CountDeferredRoutesByRun(ctx, runIDs)
	if err != nil {
		return nil, fmt.Errorf("journal: count deferred routes: %w", err)
	}
	counts := make(map[uuid.UUID]int64, len(rows))
	for _, row := range rows {
		counts[row.RunID] = row.Deferred
	}
	return counts, nil
}

// prune applies the retention caps after a run reached a terminal status.
func (s *Service) prune(ctx context.Context, environmentID *uuid.UUID) error {
	if environmentID != nil {
		if _, err := s.st.DeleteExcessTerminalRuns(ctx, store.DeleteExcessTerminalRunsParams{
			EnvironmentID: environmentID,
			Offset:        MaxRunsPerEnvironment,
		}); err != nil {
			return fmt.Errorf("journal: prune excess runs: %w", err)
		}
	}
	cutoff := maxRunAgeCutoff()
	if _, err := s.st.DeleteAgedTerminalRuns(ctx, &cutoff); err != nil {
		return fmt.Errorf("journal: prune aged runs: %w", err)
	}
	return nil
}

func guard[S comparable](machine lifecycle.Machine[S], from, to S) error {
	if !machine.Can(from, to) {
		return fmt.Errorf("%w: %v -> %v", ErrInvalidTransition, from, to)
	}
	return nil
}

// statuses spells a machine's statuses for a conditional update's guard.
func statuses[S ~string](list []S) []string {
	spelled := make([]string, len(list))
	for i, status := range list {
		spelled[i] = string(status)
	}
	return spelled
}

func notFoundOr(err error, what string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return fmt.Errorf("journal: %s: %w", what, err)
}
