package journal

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/lifecycle"
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
// change locks the row and consults the lifecycle machine before updating.
type Service struct {
	st         *store.Store
	executorID string
	broadcast  *broadcaster
	// runWatch and envRunsWatch are the payload-free invalidation planes
	// behind the run-tree and run-list SSE streams (runstream.go).
	runWatch     *signalBroadcaster
	envRunsWatch *signalBroadcaster
}

// NewService binds the journal to this boot's executor identity (a fresh
// UUID per daemon start); attempts record it so recovery can tell which
// executors no longer exist.
func NewService(st *store.Store, executorID string) *Service {
	return &Service{
		st: st, executorID: executorID, broadcast: newBroadcaster(),
		runWatch: newSignalBroadcaster(), envRunsWatch: newSignalBroadcaster(),
	}
}

func (s *Service) ExecutorID() string { return s.executorID }

type RunInput struct {
	Kind          string
	ProjectID     uuid.UUID // uuid.Nil for installation-scoped runs
	EnvironmentID uuid.UUID // uuid.Nil for runs outside an environment
	Actor         string
}

func (s *Service) CreateRun(ctx context.Context, in RunInput) (*store.Run, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("journal: generate id: %w", err)
	}
	params := store.CreateRunParams{ID: id, Kind: in.Kind, Actor: in.Actor}
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

// FinishRun moves the run to a terminal status and forces every non-terminal
// step and attempt terminal in the same transaction: running work adopts the
// run's outcome (failed or cancelled), unstarted steps are skipped. It then
// applies the retention caps.
func (s *Service) FinishRun(ctx context.Context, id uuid.UUID, to RunStatus) error {
	if !Runs.Terminal(to) {
		return fmt.Errorf("%w: finish requires a terminal status, got %s", ErrInvalidTransition, to)
	}
	closeStatus := "cancelled"
	if to == RunFailed {
		closeStatus = "failed"
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
		return q.MarkRunFinished(ctx, store.MarkRunFinishedParams{ID: id, Status: string(to)})
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
	if _, err := s.st.InsertStep(ctx, store.InsertStepParams{
		ID: id, RunID: runID, ParentID: parentID, Key: key, Title: title,
	}); err != nil {
		return nil, fmt.Errorf("journal: insert step: %w", err)
	}
	step, err := s.st.GetStepByRunAndKey(ctx, store.GetStepByRunAndKeyParams{RunID: runID, Key: key})
	if err != nil {
		return nil, fmt.Errorf("journal: read step: %w", err)
	}
	s.notifyRun(runID)
	return &step, nil
}

func (s *Service) SetStepStatus(ctx context.Context, stepID uuid.UUID, to StepStatus) error {
	var runID uuid.UUID
	err := s.st.WithTx(ctx, func(q *store.Queries) error {
		step, err := q.GetStepForUpdate(ctx, stepID)
		if err != nil {
			return notFoundOr(err, "lock step")
		}
		if err := guard(Steps, StepStatus(step.Status), to); err != nil {
			return err
		}
		runID = step.RunID
		return q.SetStepStatus(ctx, store.SetStepStatusParams{ID: stepID, Status: string(to)})
	})
	if err != nil {
		return err
	}
	s.notifyRun(runID)
	return nil
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
	var stepID uuid.UUID
	err := s.st.WithTx(ctx, func(q *store.Queries) error {
		attempt, err := q.GetAttemptForUpdate(ctx, attemptID)
		if err != nil {
			return notFoundOr(err, "lock attempt")
		}
		if err := guard(Attempts, AttemptStatus(attempt.Status), to); err != nil {
			return err
		}
		stepID = attempt.StepID
		return q.MarkAttemptFinished(ctx, store.MarkAttemptFinishedParams{ID: attemptID, Status: string(to)})
	})
	if err != nil {
		return err
	}
	s.notifyRunOfStep(ctx, stepID)
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

func notFoundOr(err error, what string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return fmt.Errorf("journal: %s: %w", what, err)
}
