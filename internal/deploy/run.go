package deploy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/redact"
	"github.com/Hinkolas/skali/internal/store"
)

// ErrDeploymentInFlight: the environment already has a running deployment.
var ErrDeploymentInFlight = errors.New("deploy: another deployment is already running for this environment")

// InFlightError is ErrDeploymentInFlight naming the run that holds the
// environment, so the refusal can say which run to follow or cancel.
// errors.Is matches it as ErrDeploymentInFlight.
type InFlightError struct {
	RunID uuid.UUID
	Kind  string
}

func (e *InFlightError) Error() string {
	return fmt.Sprintf("%v: %s run %s", ErrDeploymentInFlight, e.Kind, e.RunID)
}

func (e *InFlightError) Is(target error) bool { return target == ErrDeploymentInFlight }

// inFlight is the refusal for an environment whose running run won the
// one-running-run race: it names that run, or stays the bare
// ErrDeploymentInFlight when the run cannot be read (it finished since).
func (s *Service) inFlight(ctx context.Context, environmentID uuid.UUID) error {
	run, err := s.st.GetRunningRunByEnvironment(ctx, &environmentID)
	if err != nil {
		return ErrDeploymentInFlight
	}
	return &InFlightError{RunID: run.ID, Kind: run.Kind}
}

// preparingInFlight is the refusal for an environment with an open
// artifact window: it names the window's run.
func preparingInFlight(deployment store.Deployment) error {
	if deployment.RunID == nil {
		return ErrDeploymentInFlight
	}
	return &InFlightError{RunID: *deployment.RunID, Kind: "deployment"}
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
	// ActionPlatforms carries each application's platform string from the
	// deployment's recorded actions, so Prepare stamps the same platform
	// set onto the stored revision that the plan preview stamped onto the
	// candidate.
	ActionPlatforms map[string]string
	// redactor is the one a deployment's completion already built; without
	// it the stages build their own.
	redactor *redact.Redactor
	// resumable marks a daemon-owned completion: the service lifetime
	// ending (shutdown) leaves the deployment preparing for the next boot
	// to resume instead of failing it.
	resumable bool
	// settled lists the steps a resumed completion's interrupted first
	// attempt already concluded; the stage runs again, but its records are
	// not repeated.
	settled map[string]bool
}

type ExecuteResult struct {
	RunID      uuid.UUID
	RevisionID uuid.UUID
}

// Execute runs one deployment end to end under a journaled run: prepare
// (resolve artifacts, build and store the immutable revision), then promote
// (move the target, promote values, advance the draft) in one transaction.
// Failure discards the staged candidate and finishes the run failed; the
// target and current values are untouched by construction. Tests exercise
// it with the fake resolver; the API wires the real build and import
// resolvers.
func (s *Service) Execute(ctx context.Context, in ExecuteInput) (*ExecuteResult, error) {
	run, err := in.Journal.BeginRun(ctx, journal.RunInput{
		Kind:          "deployment",
		ProjectID:     in.ProjectID,
		EnvironmentID: in.EnvironmentID,
		Actor:         in.Actor,
	})
	if errors.Is(err, journal.ErrRunConflict) {
		return nil, s.inFlight(ctx, in.EnvironmentID)
	}
	if err != nil {
		return nil, err
	}
	return s.runStages(ctx, run.ID, in)
}

// runStages journals revision creation and promotion into an already
// running run, then hands rollout to the kernel (or, without one, finishes
// the run). Execute and the deployment completion flow share it; the
// deterministic step keys are "revision" and "promote".
func (s *Service) runStages(ctx context.Context, runID uuid.UUID, in ExecuteInput) (*ExecuteResult, error) {
	result := &ExecuteResult{RunID: runID}

	// The redactor covers the environment's current secrets plus the
	// candidate's staged ones; every log line passes through it.
	redactor := in.redactor
	if redactor == nil {
		var err error
		if redactor, err = s.values.Redactor(ctx, in.EnvironmentID, in.CandidateID); err != nil {
			return result, s.fail(ctx, in, runID, nil, err)
		}
	}

	// Each step shows running while its work runs; its log lines are
	// journaled with its outcome in one write.
	info := func(message string) journal.LogEntry { return journal.LogEntry{Level: "info", Message: message} }
	warn := func(message string) journal.LogEntry { return journal.LogEntry{Level: "warn", Message: message} }
	step := func(ctx context.Context, key, title string, to journal.StepStatus, entries []journal.LogEntry) error {
		if in.settled[key] {
			return nil
		}
		_, err := in.Journal.RecordStep(ctx, journal.StepRecord{RunID: runID, Key: key, Title: title,
			To: to, Entries: entries}, redactor)
		return err
	}

	// Step 1: create the immutable revision.
	const prepareKey, prepareTitle = "revision", "Create revision"
	if err := step(ctx, prepareKey, prepareTitle, journal.StepRunning, nil); err != nil {
		return result, s.fail(ctx, in, runID, redactor, err)
	}
	prepareLog := []journal.LogEntry{info("resolving artifacts and building the revision")}
	prepared, err := s.Prepare(ctx, PrepareInput{
		EnvironmentID:       in.EnvironmentID,
		DefinitionVersionID: in.DefinitionVersionID,
		CandidateID:         in.CandidateID,
		Resolver:            in.Resolver,
		LocalApplications:   in.LocalApplications,
		PruneValues:         in.PruneValues,
		ActionPlatforms:     in.ActionPlatforms,
	})
	if err != nil {
		if s.interrupted(in) {
			return result, fmt.Errorf("%w: %w", errInterrupted, err)
		}
		// The diagnostic must land even when ctx is what failed.
		cctx, cancel := detached(ctx)
		defer cancel()
		_ = step(cctx, prepareKey, prepareTitle, journal.StepFailed,
			append(prepareLog, journal.LogEntry{Level: "error", Message: "preparation failed: " + err.Error()}))
		return result, s.fail(cctx, in, runID, redactor, err)
	}
	for _, warning := range compiler.Warnings(prepared.Revision.Definition) {
		prepareLog = append(prepareLog, warn(warning.Code+": "+warning.Message))
	}
	result.RevisionID = prepared.RevisionID
	if len(prepared.Orphaned) > 0 {
		prepareLog = append(prepareLog, warn("ignoring stored values not referenced by this definition: "+strings.Join(prepared.Orphaned, ", ")))
	}
	prepareLog = append(prepareLog, info("revision "+prepared.Revision.Checksum+" stored"))
	if err := step(ctx, prepareKey, prepareTitle, journal.StepSucceeded, prepareLog); err != nil {
		return result, s.fail(ctx, in, runID, redactor, err)
	}

	// Step 2: promote atomically.
	const promoteKey, promoteTitle = "promote", "Promote revision"
	if err := step(ctx, promoteKey, promoteTitle, journal.StepRunning, nil); err != nil {
		return result, s.fail(ctx, in, runID, redactor, err)
	}
	prepared.Restart = in.Restart
	prepared.DeploymentID = in.DeploymentID
	if s.enqueuer != nil {
		// Only a kernel adopts the run; without one it concludes here.
		prepared.RunID = runID
	}
	var promoteLog []journal.LogEntry
	if in.Restart {
		promoteLog = append(promoteLog, info("forced deployment: application workloads will restart"))
	}
	if len(prepared.Pruned) > 0 {
		promoteLog = append(promoteLog, info("pruning stored values not referenced by this definition: "+strings.Join(prepared.Pruned, ", ")))
	}
	if err := s.Promote(ctx, prepared); err != nil {
		if s.interrupted(in) {
			return result, fmt.Errorf("%w: %w", errInterrupted, err)
		}
		cctx, cancel := detached(ctx)
		defer cancel()
		_ = step(cctx, promoteKey, promoteTitle, journal.StepFailed,
			append(promoteLog, journal.LogEntry{Level: "error", Message: "promotion failed: " + err.Error()}))
		return result, s.fail(cctx, in, runID, redactor, err)
	}
	// The run says what the stored revision changed against the one it
	// replaced. Explanatory only: a failed write costs the history its
	// summary, never the deployment, and a deployment that never promoted
	// keeps the plan it set out with.
	if err := s.recordPromotion(ctx, in.Journal, runID, prepared, in.Restart); err != nil {
		slog.WarnContext(ctx, "deploy: record the run's plan", "run", runID, "err", err)
	}
	promoteLog = append(promoteLog, info("target set to revision "+prepared.Revision.Checksum))
	if err := unlessConcluded(step(ctx, promoteKey, promoteTitle, journal.StepSucceeded, promoteLog)); err != nil {
		return result, s.fail(ctx, in, runID, redactor, err)
	}

	// With a kernel wired, the run stays running: the reconcile worker owns
	// apply, verify, and activation, journaling into this same run through
	// deterministic step keys, and finishes it. Without one (kept for
	// tests), promotion concludes the run.
	if s.enqueuer != nil {
		if _, err := in.Journal.EnsureStep(ctx, runID, nil, "rollout", "Roll out revision"); unlessConcluded(err) != nil {
			return result, s.fail(ctx, in, runID, redactor, err)
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
// were only ever staged), finish the run failed with the cause as its
// reason, and return the original error. The journal steps were already
// closed by the caller where one was active; the journal forces the rest
// terminal. The reason passes through redactor (nil before one exists)
// because a prepare or promote error can echo staged secrets. It runs
// detached from ctx's cancellation: the cause may well be that ctx died,
// and a run that stays running would wedge the environment. The exception
// is a resumable completion interrupted by shutdown, which keeps its
// candidate and run for the next boot.
func (s *Service) fail(ctx context.Context, in ExecuteInput, runID uuid.UUID, redactor *redact.Redactor, cause error) error {
	if s.interrupted(in) {
		return fmt.Errorf("%w: %w", errInterrupted, cause)
	}
	ctx, cancel := detached(ctx)
	defer cancel()
	if in.CandidateID != uuid.Nil {
		if err := s.values.DiscardCandidate(ctx, in.EnvironmentID, in.CandidateID); err != nil {
			slog.WarnContext(ctx, "deploy: discard staged candidate", "run", runID, "err", err)
		}
	}
	if err := in.Journal.FailRun(ctx, runID, redactor, cause.Error()); err != nil &&
		!errors.Is(err, journal.ErrInvalidTransition) {
		return fmt.Errorf("deploy: finish run after failure: %w (original: %w)", err, cause)
	}
	return cause
}

// interrupted reports whether a stage error belongs to a resumable
// completion that shutdown stopped. Before promotion nothing it did needs
// undoing: the revision is reused by identical inputs, and the journal
// reattaches its steps by key. After promotion the kernel adopts the run.
func (s *Service) interrupted(in ExecuteInput) bool {
	return in.resumable && s.lifetime().Err() != nil
}
