package deploy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/journal"
)

// ErrDeploymentInFlight: the environment already has a running deployment.
var ErrDeploymentInFlight = errors.New("deploy: another deployment is already running for this environment")

// discardUnstartedRun removes a run whose start lost the environment's
// running-run race. The row explains nothing, nothing will ever finish it,
// and the journal's retention only reclaims terminal runs, so abandoning it
// would leave a pending run in every reader's view forever.
func discardUnstartedRun(ctx context.Context, jr *journal.Service, id uuid.UUID) {
	if err := jr.DiscardRun(ctx, id); err != nil {
		slog.WarnContext(ctx, "discard unstarted run", "run", id, "error", err)
	}
}

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
	// Restart stamps a workload restart at promotion (a forced deployment):
	// application pods are recreated even when the revision is unchanged.
	Restart bool
	// DeploymentID is the artifact-window row this execution closes;
	// promotion marks it promoted in the transaction that moves the target.
	// uuid.Nil runs the stages without a deployment row.
	DeploymentID uuid.UUID
	// LocalApplications is the intercept set this deploy promotes: host-run
	// applications that resolve no artifact. Promotion replaces the
	// environment's stored intercepts with it; empty clears them.
	LocalApplications map[string]LocalApplication
	// PruneValues unsets the stored values the definition no longer
	// references at promotion instead of ignoring them.
	PruneValues bool
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
		discardUnstartedRun(ctx, in.Journal, run.ID)
		if errors.Is(err, journal.ErrRunConflict) {
			return nil, ErrDeploymentInFlight
		}
		return nil, err
	}
	return s.runStages(ctx, run.ID, in)
}

// runStages journals revision creation and promotion into an already
// running run, then hands rollout to the kernel (or, without one, finishes
// the run). Execute and the R3 deployment completion flow share it; the
// deterministic step keys are "revision" and "promote".
func (s *Service) runStages(ctx context.Context, runID uuid.UUID, in ExecuteInput) (*ExecuteResult, error) {
	result := &ExecuteResult{RunID: runID}

	// The redactor covers the environment's current secrets plus the
	// candidate's staged ones; every log line passes through it.
	redactor, err := s.values.Redactor(ctx, in.EnvironmentID, in.CandidateID)
	if err != nil {
		return result, s.fail(ctx, in, runID, nil, err)
	}

	// Step 1: create the immutable revision.
	prepareStep, err := in.Journal.EnsureStep(ctx, runID, nil, "revision", "Create revision")
	if err != nil {
		return result, s.fail(ctx, in, runID, nil, err)
	}
	if err := in.Journal.SetStepStatus(ctx, prepareStep.ID, journal.StepRunning); err != nil {
		return result, s.fail(ctx, in, runID, nil, err)
	}
	attempt, err := in.Journal.StartAttempt(ctx, prepareStep.ID)
	if err != nil {
		return result, s.fail(ctx, in, runID, nil, err)
	}
	writer := in.Journal.Writer(attempt.ID, redactor)

	_ = writer.Info(ctx, "resolving artifacts and building the revision")
	prepared, err := s.Prepare(ctx, PrepareInput{
		EnvironmentID:       in.EnvironmentID,
		DefinitionVersionID: in.DefinitionVersionID,
		CandidateID:         in.CandidateID,
		Resolver:            in.Resolver,
		LocalApplications:   in.LocalApplications,
		PruneValues:         in.PruneValues,
	})
	if err != nil {
		_ = writer.Error(ctx, "preparation failed: "+err.Error())
		_ = in.Journal.FinishAttempt(ctx, attempt.ID, journal.AttemptFailed)
		_ = in.Journal.SetStepStatus(ctx, prepareStep.ID, journal.StepFailed)
		return result, s.fail(ctx, in, runID, writer, err)
	}
	result.RevisionID = prepared.RevisionID
	if len(prepared.Orphaned) > 0 {
		_ = writer.Warn(ctx, "ignoring stored values not referenced by this definition: "+strings.Join(prepared.Orphaned, ", "))
	}
	_ = writer.Info(ctx, "revision "+prepared.Revision.Checksum+" stored")
	if err := in.Journal.FinishAttempt(ctx, attempt.ID, journal.AttemptSucceeded); err != nil {
		return result, s.fail(ctx, in, runID, nil, err)
	}
	if err := in.Journal.SetStepStatus(ctx, prepareStep.ID, journal.StepSucceeded); err != nil {
		return result, s.fail(ctx, in, runID, nil, err)
	}

	// Step 2: promote atomically.
	promoteStep, err := in.Journal.EnsureStep(ctx, runID, nil, "promote", "Promote revision")
	if err != nil {
		return result, s.fail(ctx, in, runID, nil, err)
	}
	if err := in.Journal.SetStepStatus(ctx, promoteStep.ID, journal.StepRunning); err != nil {
		return result, s.fail(ctx, in, runID, nil, err)
	}
	promoteAttempt, err := in.Journal.StartAttempt(ctx, promoteStep.ID)
	if err != nil {
		return result, s.fail(ctx, in, runID, nil, err)
	}
	promoteWriter := in.Journal.Writer(promoteAttempt.ID, redactor)
	prepared.Restart = in.Restart
	prepared.DeploymentID = in.DeploymentID
	if in.Restart {
		_ = promoteWriter.Info(ctx, "forced deployment: application workloads will restart")
	}
	if len(prepared.Pruned) > 0 {
		_ = promoteWriter.Info(ctx, "pruning stored values not referenced by this definition: "+strings.Join(prepared.Pruned, ", "))
	}
	if err := s.Promote(ctx, prepared); err != nil {
		_ = promoteWriter.Error(ctx, "promotion failed: "+err.Error())
		_ = in.Journal.FinishAttempt(ctx, promoteAttempt.ID, journal.AttemptFailed)
		_ = in.Journal.SetStepStatus(ctx, promoteStep.ID, journal.StepFailed)
		return result, s.fail(ctx, in, runID, promoteWriter, err)
	}
	_ = promoteWriter.Info(ctx, "target set to revision "+prepared.Revision.Checksum)
	if err := in.Journal.FinishAttempt(ctx, promoteAttempt.ID, journal.AttemptSucceeded); err != nil {
		return result, s.fail(ctx, in, runID, nil, err)
	}
	if err := in.Journal.SetStepStatus(ctx, promoteStep.ID, journal.StepSucceeded); err != nil {
		return result, s.fail(ctx, in, runID, nil, err)
	}

	// With a kernel wired, the run stays running: the reconcile worker owns
	// apply, verify, and activation, journaling into this same run through
	// deterministic step keys, and finishes it. Without one (R1 behavior,
	// kept for tests), promotion concludes the run.
	if s.enqueuer != nil {
		if _, err := in.Journal.EnsureStep(ctx, runID, nil, "rollout", "Roll out revision"); err != nil {
			return result, s.fail(ctx, in, runID, nil, err)
		}
		s.enqueuer.Enqueue(in.EnvironmentID)
		return result, nil
	}
	if err := in.Journal.FinishRun(ctx, runID, journal.RunSucceeded); err != nil {
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
