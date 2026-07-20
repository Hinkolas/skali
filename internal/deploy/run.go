package deploy

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/journal"
)

// ErrDeploymentInFlight: the environment already has a running deployment.
var ErrDeploymentInFlight = errors.New("deploy: another deployment is already running for this environment")

// ExecuteInput describes one deployment execution. The journal is passed in
// rather than owned so the run tree and the deployment stay decoupled:
// deleting the journal never affects deployment behavior.
type ExecuteInput struct {
	ProjectID           uuid.UUID
	EnvironmentID       uuid.UUID
	DefinitionVersionID uuid.UUID
	CandidateID         uuid.UUID // uuid.Nil deploys current values
	Resolver            ArtifactResolver
	Journal             *journal.Service
	Actor               string
}

type ExecuteResult struct {
	RunID      uuid.UUID
	RevisionID uuid.UUID
}

// Execute runs one deployment end to end under a journaled run: prepare
// (resolve artifacts, build and store the immutable revision), then promote
// (move the target, promote values, advance the draft) in one transaction.
// Failure discards the staged candidate and finishes the run failed; the
// target and current values are untouched by construction. In R1 this is
// exercised by tests with the fake resolver; R3 wires it to the API and
// real build/import resolvers.
func (s *Service) Execute(ctx context.Context, in ExecuteInput) (*ExecuteResult, error) {
	run, err := in.Journal.CreateRun(ctx, journal.RunInput{
		Kind:          "deployment",
		ProjectID:     in.ProjectID,
		EnvironmentID: in.EnvironmentID,
		Actor:         in.Actor,
	})
	if err != nil {
		return nil, err
	}
	if err := in.Journal.StartRun(ctx, run.ID); err != nil {
		if errors.Is(err, journal.ErrRunConflict) {
			return nil, ErrDeploymentInFlight
		}
		return nil, err
	}
	result := &ExecuteResult{RunID: run.ID}

	// The redactor covers the environment's current secrets plus the
	// candidate's staged ones; every log line passes through it.
	redactor, err := s.values.Redactor(ctx, in.EnvironmentID, in.CandidateID)
	if err != nil {
		return result, s.fail(ctx, in, run.ID, nil, err)
	}

	// Step 1: prepare the revision.
	prepareStep, err := in.Journal.EnsureStep(ctx, run.ID, nil, "prepare", "Prepare revision")
	if err != nil {
		return result, s.fail(ctx, in, run.ID, nil, err)
	}
	if err := in.Journal.SetStepStatus(ctx, prepareStep.ID, journal.StepRunning); err != nil {
		return result, s.fail(ctx, in, run.ID, nil, err)
	}
	attempt, err := in.Journal.StartAttempt(ctx, prepareStep.ID)
	if err != nil {
		return result, s.fail(ctx, in, run.ID, nil, err)
	}
	writer := in.Journal.Writer(attempt.ID, redactor)

	_ = writer.Info(ctx, "resolving artifacts and building the revision")
	prepared, err := s.Prepare(ctx, PrepareInput{
		EnvironmentID:       in.EnvironmentID,
		DefinitionVersionID: in.DefinitionVersionID,
		CandidateID:         in.CandidateID,
		Resolver:            in.Resolver,
	})
	if err != nil {
		_ = writer.Error(ctx, "preparation failed: "+err.Error())
		_ = in.Journal.FinishAttempt(ctx, attempt.ID, journal.AttemptFailed)
		_ = in.Journal.SetStepStatus(ctx, prepareStep.ID, journal.StepFailed)
		return result, s.fail(ctx, in, run.ID, writer, err)
	}
	result.RevisionID = prepared.RevisionID
	_ = writer.Info(ctx, "revision "+prepared.Revision.Checksum+" stored")
	if err := in.Journal.FinishAttempt(ctx, attempt.ID, journal.AttemptSucceeded); err != nil {
		return result, s.fail(ctx, in, run.ID, nil, err)
	}
	if err := in.Journal.SetStepStatus(ctx, prepareStep.ID, journal.StepSucceeded); err != nil {
		return result, s.fail(ctx, in, run.ID, nil, err)
	}

	// Step 2: promote atomically.
	promoteStep, err := in.Journal.EnsureStep(ctx, run.ID, nil, "promote", "Promote revision")
	if err != nil {
		return result, s.fail(ctx, in, run.ID, nil, err)
	}
	if err := in.Journal.SetStepStatus(ctx, promoteStep.ID, journal.StepRunning); err != nil {
		return result, s.fail(ctx, in, run.ID, nil, err)
	}
	promoteAttempt, err := in.Journal.StartAttempt(ctx, promoteStep.ID)
	if err != nil {
		return result, s.fail(ctx, in, run.ID, nil, err)
	}
	promoteWriter := in.Journal.Writer(promoteAttempt.ID, redactor)
	if err := s.Promote(ctx, prepared); err != nil {
		_ = promoteWriter.Error(ctx, "promotion failed: "+err.Error())
		_ = in.Journal.FinishAttempt(ctx, promoteAttempt.ID, journal.AttemptFailed)
		_ = in.Journal.SetStepStatus(ctx, promoteStep.ID, journal.StepFailed)
		return result, s.fail(ctx, in, run.ID, promoteWriter, err)
	}
	_ = promoteWriter.Info(ctx, "target set to revision "+prepared.Revision.Checksum)
	if err := in.Journal.FinishAttempt(ctx, promoteAttempt.ID, journal.AttemptSucceeded); err != nil {
		return result, s.fail(ctx, in, run.ID, nil, err)
	}
	if err := in.Journal.SetStepStatus(ctx, promoteStep.ID, journal.StepSucceeded); err != nil {
		return result, s.fail(ctx, in, run.ID, nil, err)
	}
	if err := in.Journal.FinishRun(ctx, run.ID, journal.RunSucceeded); err != nil {
		return result, err
	}
	return result, nil
}

// fail is the single failure path: discard the staged candidate (its rows
// were only ever staged), finish the run failed, and return the original
// error. The journal steps were already closed by the caller where one was
// active; FinishRun forces the rest terminal.
func (s *Service) fail(ctx context.Context, in ExecuteInput, runID uuid.UUID, writer *journal.Writer, cause error) error {
	if in.CandidateID != uuid.Nil {
		if err := s.values.DiscardCandidate(ctx, in.EnvironmentID, in.CandidateID); err != nil {
			_ = writer.Error(ctx, "discarding the staged candidate failed: "+err.Error())
		}
	}
	if err := in.Journal.FinishRun(ctx, runID, journal.RunFailed); err != nil &&
		!errors.Is(err, journal.ErrInvalidTransition) {
		return fmt.Errorf("deploy: finish run after failure: %w (original: %w)", err, cause)
	}
	return cause
}
