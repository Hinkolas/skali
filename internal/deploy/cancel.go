package deploy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/store"
)

// ErrRevisionActive: the rollout a cancel would stop already activated its
// revision, which a cancel no longer undoes.
var ErrRevisionActive = errors.New("deploy: the revision is already active")

// RunConcludedError: the run finished before a cancel could stop it.
type RunConcludedError struct{ Status string }

func (e *RunConcludedError) Error() string { return "deploy: the run is already " + e.Status }

// CancelRolloutInput names a run whose promoted rollout a cancel stops.
type CancelRolloutInput struct {
	RunID         uuid.UUID
	EnvironmentID uuid.UUID
	// RevisionID is the revision the run rolls out, a deployment's own;
	// nil takes the environment's target, which a rollback set.
	RevisionID *uuid.UUID
	Journal    *journal.Service
}

// CancelOutcome is what a cancel did to the environment's target.
type CancelOutcome struct {
	// Fallback: the target returned to the active revision.
	Fallback bool
	// Continues: the environment had no active revision to return to, so
	// the run's revision stays the target and its rollout goes on without
	// a run until the next deployment replaces it.
	Continues bool
}

// CancelRollout stops a promoted rollout. Under the environment lock the
// target returns to the active revision and the run finishes cancelled, in
// one transaction, so neither happens without the other. A lock held for
// the whole request wait answers ErrEnvironmentBusy and changes nothing,
// so the cancel can be retried. A rollout that already activated answers
// ErrRevisionActive, and a run that already finished *RunConcludedError.
func (s *Service) CancelRollout(ctx context.Context, in CancelRolloutInput) (CancelOutcome, error) {
	// A client that disconnects while the cancel waits or writes must not
	// leave it half done.
	ctx, cancel := detached(ctx)
	defer cancel()
	unlock, err := s.st.LockEnvironmentWithin(ctx, in.EnvironmentID, s.requestWait)
	if err != nil {
		return CancelOutcome{}, err
	}
	defer unlock()

	// Under the lock no pass is between its reads and its writes, so what
	// is read here stays true until the commit.
	run, err := in.Journal.Run(ctx, in.RunID)
	if err != nil {
		return CancelOutcome{}, err
	}
	if journal.Runs.Terminal(journal.RunStatus(run.Status)) {
		return CancelOutcome{}, &RunConcludedError{Status: run.Status}
	}
	target, err := s.st.GetEnvironmentTarget(ctx, in.EnvironmentID)
	if err != nil {
		return CancelOutcome{}, fmt.Errorf("deploy: get environment target: %w", err)
	}
	revisionID := in.RevisionID
	if revisionID == nil {
		revisionID = target.TargetRevisionID
	}
	var routes []compiler.ResolvedRoute
	var outcome CancelOutcome
	switch {
	case revisionID != nil && sameRevision(target.ActiveRevisionID, *revisionID):
		return CancelOutcome{}, ErrRevisionActive
	case revisionID == nil || !sameRevision(target.TargetRevisionID, *revisionID):
		// The target no longer holds the run's revision: there is nothing of
		// the run's to undo, only the run to conclude.
	case target.ActiveRevisionID == nil:
		// A first deployment has nothing to return to. Clearing the target
		// would leave its workloads running unreconciled, so the target
		// stays, as when its rollout deadline passes.
		outcome.Continues = true
	default:
		active, err := s.GetRevision(ctx, *target.ActiveRevisionID)
		if err != nil {
			return CancelOutcome{}, err
		}
		// Falling back claims the active revision's hostnames again: one
		// the rollout released may have gone to another environment since.
		if routes, err = s.revisionRoutes(ctx, in.EnvironmentID, active); err != nil {
			return CancelOutcome{}, err
		}
		outcome.Fallback = true
	}

	var concluded func(context.Context) error
	err = s.st.WithTx(ctx, func(q *store.Queries) error {
		if outcome.Fallback {
			rows, err := q.FallbackEnvironmentTarget(ctx, store.FallbackEnvironmentTargetParams{
				EnvironmentID: in.EnvironmentID, TargetRevisionID: revisionID,
			})
			if err != nil {
				return fmt.Errorf("deploy: fall back target: %w", err)
			}
			if outcome.Fallback = rows > 0; outcome.Fallback {
				if err := s.claimRoutesTx(ctx, q, in.EnvironmentID, *target.ActiveRevisionID, routes); err != nil {
					return err
				}
			}
		}
		var err error
		concluded, err = in.Journal.FinishRunTx(ctx, q, in.RunID, journal.RunCancelled)
		return err
	})
	if errors.Is(err, journal.ErrInvalidTransition) {
		// The run's own work finished it (a failed promotion) after the read
		// above; the fallback rolled back with the refused finish.
		if run, readErr := in.Journal.Run(ctx, in.RunID); readErr == nil {
			return CancelOutcome{}, &RunConcludedError{Status: run.Status}
		}
	}
	if err != nil {
		return CancelOutcome{}, err
	}
	if err := concluded(ctx); err != nil {
		// The cancel committed; only the retention caps are left for the
		// next finish to apply.
		slog.WarnContext(ctx, "cancel: conclude run", "run", in.RunID, "error", err)
	}
	return outcome, nil
}

func sameRevision(id *uuid.UUID, want uuid.UUID) bool {
	return id != nil && *id == want
}
