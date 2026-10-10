package journal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

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
	// ErrRunFinished: the run has finished, or is gone, so no step or
	// attempt is added under it any more. It is an invalid transition, so
	// callers that tolerate a refused step write tolerate this one too.
	ErrRunFinished = fmt.Errorf("%w: the run has finished", ErrInvalidTransition)
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
	params := store.CreateRunParams(runParams(id, in))
	run, err := s.st.CreateRun(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("journal: create run: %w", err)
	}
	s.notifyEnvironment(params.EnvironmentID)
	return &run, nil
}

// BeginRun creates a run already running, in one statement. An environment
// that already has a running run refuses it with ErrRunConflict, and then
// no row exists.
func (s *Service) BeginRun(ctx context.Context, in RunInput) (*store.Run, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("journal: generate id: %w", err)
	}
	params := store.BeginRunParams(runParams(id, in))
	run, err := s.st.BeginRun(ctx, params)
	if store.IsUniqueViolation(err) {
		return nil, ErrRunConflict
	}
	if err != nil {
		return nil, fmt.Errorf("journal: begin run: %w", err)
	}
	s.notifyRun(run.ID)
	s.notifyEnvironment(params.EnvironmentID)
	return &run, nil
}

func runParams(id uuid.UUID, in RunInput) store.CreateRunParams {
	params := store.CreateRunParams{ID: id, Kind: in.Kind, Actor: in.Actor, BypassProtection: in.BypassProtection}
	if in.ProjectID != uuid.Nil {
		params.ProjectID = &in.ProjectID
	}
	if in.EnvironmentID != uuid.Nil {
		params.EnvironmentID = &in.EnvironmentID
	}
	return params
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

// MaxFailureBytes bounds a run's failure reason; longer reasons are cut with
// a truncation suffix. The reason is a one-line summary, never the detail.
const MaxFailureBytes = 512

// FinishRun moves the run to a terminal status and forces every non-terminal
// step and attempt terminal in the same statement: running work adopts the
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
	environmentID, err := s.st.FinishRun(ctx, store.FinishRunParams{
		ID: id, Status: string(to), FromStatuses: statuses(Runs.Sources(to)),
		Failure: failure, CloseStatus: closeStatus,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		run, err := s.st.GetRunByID(ctx, id)
		if err != nil {
			return notFoundOr(err, "get run")
		}
		return fmt.Errorf("%w: %v -> %v", ErrInvalidTransition, run.Status, to)
	}
	if err != nil {
		return fmt.Errorf("journal: finish run: %w", err)
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
		// Another writer created it after the statement's snapshot, or the
		// run has finished and none exists.
		step, err = s.st.GetStepByRunAndKey(ctx, store.GetStepByRunAndKeyParams{RunID: runID, Key: key})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrRunFinished
		}
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

// LogEntry is one log line of a step journaled by RecordStep.
type LogEntry struct {
	Level   string
	Message string
	Fields  map[string]any
}

// StepRecord is one observation of a step, addressed by its key.
type StepRecord struct {
	RunID    uuid.UUID
	ParentID *uuid.UUID
	Key      string
	Title    string
	// To is the status the step moves to.
	To StepStatus
	// Entries are the lines of the finished attempt the record writes; it
	// fails when To is failed and succeeds otherwise. A record to running
	// writes no attempt and carries no entries.
	Entries []LogEntry
	// Dedupe writes the attempt only when the last entry differs from the
	// step's latest line, so an observation repeated across passes adds
	// nothing.
	Dedupe bool
}

// StepState is a step as the journal last saw it: its status and, while it
// has not started, its latest line.
type StepState struct {
	ID     uuid.UUID
	Status StepStatus
	Latest string
}

// StepStates reads every step of a run, keyed by step key.
func (s *Service) StepStates(ctx context.Context, runID uuid.UUID) (map[string]StepState, error) {
	rows, err := s.st.ListStepStates(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("journal: list steps: %w", err)
	}
	states := make(map[string]StepState, len(rows))
	for _, row := range rows {
		states[row.Key] = StepState{ID: row.ID, Status: StepStatus(row.Status), Latest: row.Latest}
	}
	return states, nil
}

// RecordStep journals one observation of a step in one statement: it
// creates the step when the run has none under the key, moves it to To the
// way the step machine allows, through running when the machine goes that
// way, and writes one finished attempt carrying the entries, redacted and
// bounded like Append's, unless To is running. A step whose status does not
// allow the change is refused with ErrInvalidTransition, and one with a
// running attempt with ErrAttemptConflict, and every record under a
// finished run with ErrRunFinished; none writes anything. The returned
// state is the step's after the record, or as found when it was refused.
func (s *Service) RecordStep(ctx context.Context, record StepRecord, redactor *redact.Redactor) (StepState, error) {
	if record.To == StepRunning && len(record.Entries) > 0 {
		return StepState{}, fmt.Errorf("journal: record step: a step that starts writes no lines")
	}
	attemptStatus := AttemptSucceeded
	if record.To == StepFailed {
		attemptStatus = AttemptFailed
	}
	lines, encoded, err := encodeEntries(redactor, record.Entries)
	if err != nil {
		return StepState{}, err
	}
	from := recordSources(record.To)
	var row store.RecordStepRow
	// A step created by another writer after the statement's snapshot is
	// neither found nor created; the second statement sees it.
	for range 2 {
		stepID, err := uuid.NewV7()
		if err != nil {
			return StepState{}, fmt.Errorf("journal: generate id: %w", err)
		}
		attemptID, err := uuid.NewV7()
		if err != nil {
			return StepState{}, fmt.Errorf("journal: generate id: %w", err)
		}
		row, err = s.st.RecordStep(ctx, store.RecordStepParams{
			ID: stepID, RunID: record.RunID, ParentID: record.ParentID, Key: record.Key, Title: record.Title,
			Status: string(record.To), FromStatuses: statuses(from), Dedupe: record.Dedupe,
			AttemptID: attemptID, AttemptStatus: string(attemptStatus), ExecutorID: s.executorID,
			Entries: encoded,
		})
		if err != nil {
			return StepState{}, fmt.Errorf("journal: record step: %w", err)
		}
		if row.Written || row.PreviousStatus != "" || !row.RunOpen {
			break
		}
	}
	previous := StepStatus(row.PreviousStatus)
	if !row.Written {
		switch {
		case !row.RunOpen:
			return StepState{ID: row.StepID, Status: previous}, ErrRunFinished
		case previous == "":
			return StepState{}, fmt.Errorf("journal: record step: %s neither found nor created", record.Key)
		case !slices.Contains(from, previous):
			return StepState{ID: row.StepID, Status: previous}, fmt.Errorf("%w: %v -> %v", ErrInvalidTransition, previous, record.To)
		case row.AttemptRunning:
			return StepState{ID: row.StepID, Status: previous}, ErrAttemptConflict
		}
		// Already at To, and the line unchanged.
		return StepState{ID: row.StepID, Status: previous, Latest: latestLine(lines)}, nil
	}
	if row.AttemptNumber > 0 {
		s.publishLines(row.StepID, row.AttemptNumber, row.LoggedAt, lines)
	}
	s.notifyRun(record.RunID)
	return StepState{ID: row.StepID, Status: record.To, Latest: latestLine(lines)}, nil
}

// recordSources are the statuses a step reaches to from in one record: the
// machine's sources, theirs as well when the machine goes through running,
// and to itself while it is not terminal, so a waiting step can note a new
// line.
func recordSources(to StepStatus) []StepStatus {
	from := Steps.Sources(to)
	if slices.Contains(from, StepRunning) {
		from = append(from, Steps.Sources(StepRunning)...)
	}
	if !Steps.Terminal(to) {
		from = append(from, to)
	}
	slices.Sort(from)
	return slices.Compact(from)
}

// entryLine is one log line as the journal stores it.
type entryLine struct {
	ID      uuid.UUID       `json:"id"`
	Level   string          `json:"level"`
	Message string          `json:"message"`
	Fields  json.RawMessage `json:"fields"`
}

// encodeEntries redacts and bounds entries like Append and encodes them as
// the JSON array the single-statement step writes insert.
func encodeEntries(redactor *redact.Redactor, entries []LogEntry) ([]entryLine, []byte, error) {
	lines := make([]entryLine, 0, len(entries))
	for _, entry := range entries {
		if !validLevel(entry.Level) {
			return nil, nil, fmt.Errorf("journal: invalid log level %q", entry.Level)
		}
		fields, err := encodeFields(redactor, entry.Fields)
		if err != nil {
			return nil, nil, err
		}
		id, err := uuid.NewV7()
		if err != nil {
			return nil, nil, fmt.Errorf("journal: generate id: %w", err)
		}
		lines = append(lines, entryLine{ID: id, Level: entry.Level, Message: boundEntry(redactor.Redact(entry.Message)), Fields: fields})
	}
	if len(lines) > MaxEntriesPerAttempt {
		lines = lines[:MaxEntriesPerAttempt]
		lines[MaxEntriesPerAttempt-1] = entryLine{ID: lines[MaxEntriesPerAttempt-1].ID, Level: "warn",
			Message: "log truncated: entry cap reached", Fields: json.RawMessage("{}")}
	}
	encoded, err := json.Marshal(lines)
	if err != nil {
		return nil, nil, fmt.Errorf("journal: encode entries: %w", err)
	}
	return lines, encoded, nil
}

// latestLine is the message the step's latest line holds after lines were
// written, empty for none.
func latestLine(lines []entryLine) string {
	if len(lines) == 0 {
		return ""
	}
	return lines[len(lines)-1].Message
}

// publishLines streams the lines of an attempt written in one statement.
func (s *Service) publishLines(stepID uuid.UUID, attempt int64, at time.Time, lines []entryLine) {
	for i, line := range lines {
		s.broadcast.Publish(stepID, LogEvent{
			StepID:        stepID,
			AttemptNumber: attempt,
			Seq:           int64(i + 1),
			TS:            at,
			Level:         line.Level,
			Message:       line.Message,
			Fields:        line.Fields,
		})
	}
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
// empty when none exist.
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
// Under a finished run it opens none and returns ErrRunFinished.
func (s *Service) StartAttempt(ctx context.Context, stepID uuid.UUID) (*store.Attempt, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("journal: generate id: %w", err)
	}
	attempt, err := s.st.CreateAttempt(ctx, store.CreateAttemptParams{
		ID: id, StepID: stepID, ExecutorID: s.executorID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrRunFinished
	}
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
	if err := s.st.PruneRuns(ctx, store.PruneRunsParams{
		EnvironmentID: environmentID,
		Keep:          MaxRunsPerEnvironment,
		Cutoff:        maxRunAgeCutoff(),
	}); err != nil {
		return fmt.Errorf("journal: prune runs: %w", err)
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
