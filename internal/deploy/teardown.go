package deploy

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/store"
)

// ErrEnvironmentReleasing: the environment is already being purged; the
// decision is one-way, so nothing else may touch it anymore.
var ErrEnvironmentReleasing = errors.New("deploy: environment is releasing")

// Environment target states (environment_targets.state). Active is the
// normal state; down and releasing are the persisted destructive decisions
// the reconciler executes.
const (
	EnvironmentStateActive    = "active"
	EnvironmentStateDown      = "down"
	EnvironmentStateReleasing = "releasing"
)

// Run kinds of the two teardown decisions. A purge journals under its own
// kind so clients can tell a deletion from a stop.
const (
	RunKindTeardown = "teardown"
	RunKindPurge    = "purge"
)

// DeleteEnvironment removes a released environment's row; the foreign keys
// cascade every environment-scoped record with it. Only the reconciler
// calls this, and only after the cluster confirms complete absence. The
// last environment of a deleting project takes the project row with it.
func (s *Service) DeleteEnvironment(ctx context.Context, environmentID uuid.UUID) error {
	env, err := s.st.GetEnvironmentByID(ctx, environmentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("deploy: get environment: %w", err)
	}
	err = s.st.WithTx(ctx, func(q *store.Queries) error {
		// The project lock serializes concluding purges: two of them
		// committing side by side would each still see the other's
		// environment and leave the project row behind.
		if _, err := q.GetProjectForUpdate(ctx, env.ProjectID); err != nil {
			return fmt.Errorf("deploy: lock project: %w", err)
		}
		if _, err := q.DeleteEnvironmentByID(ctx, environmentID); err != nil {
			return fmt.Errorf("deploy: delete environment: %w", err)
		}
		if _, err := q.DeleteDeletingProjectIfEmpty(ctx, env.ProjectID); err != nil {
			return fmt.Errorf("deploy: delete emptied project: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.feed.Publish(env.ProjectID)
	return nil
}

// Teardown persists the destructive decision behind skali dev down and asks
// the kernel to execute it. Down keeps the namespace, volumes, values, and
// history; purge releases the environment completely, ending with the
// namespace and the environment row deleted. The returned run is the
// journaled teardown the caller can attach to. Repeating a decision that is
// still executing changes nothing: the running run of that decision is
// returned as existing, so a retry attaches instead of restarting it.
func (s *Service) Teardown(ctx context.Context, environmentID uuid.UUID, purge bool, jr *journal.Service, actor string) (run *store.Run, existing bool, err error) {
	unlock, lockErr := s.st.LockEnvironmentWithin(ctx, environmentID, s.requestWait)
	if lockErr != nil {
		return nil, false, lockErr
	}
	defer unlock()
	env, err := s.st.GetEnvironmentByID(ctx, environmentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, ErrEnvironmentNotFound
		}
		return nil, false, fmt.Errorf("deploy: get environment: %w", err)
	}

	// An open deployment window means a client is still building against
	// this environment; tearing the target down underneath it would turn
	// its completion into a resurrection race. Refuse instead.
	if _, err := s.st.GetPreparingDeploymentForEnvironment(ctx, environmentID); err == nil {
		return nil, false, ErrDeploymentInFlight
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("deploy: check in-flight deployment: %w", err)
	}

	target, err := s.st.GetEnvironmentTarget(ctx, environmentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, ErrEnvironmentNotFound
		}
		return nil, false, fmt.Errorf("deploy: get environment target: %w", err)
	}
	// Releasing is one-way; refuse a stop before anything below touches
	// the purge run.
	if target.State == EnvironmentStateReleasing && !purge {
		return nil, false, ErrEnvironmentReleasing
	}
	kind, state := RunKindTeardown, EnvironmentStateDown
	if purge {
		kind, state = RunKindPurge, EnvironmentStateReleasing
	}
	running, err := s.st.GetRunningRunByEnvironment(ctx, &environmentID)
	switch {
	case err == nil && running.Kind == kind && target.State == state:
		// The same decision is still executing: hand out its run.
		return &running, true, nil
	case err == nil:
		// Any other running run (a rollout the kernel is still converging,
		// or the stop a removal overrides) is cancelled so the partial
		// unique index admits the teardown run. Journal only: the pointers
		// below are what actually stops the rollout.
		if err := jr.FinishRun(ctx, running.ID, journal.RunCancelled); err != nil &&
			!errors.Is(err, journal.ErrInvalidTransition) {
			return nil, false, fmt.Errorf("deploy: cancel running run: %w", err)
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, false, fmt.Errorf("deploy: check running run: %w", err)
	}

	if purge {
		rows, err := s.st.MarkEnvironmentReleasing(ctx, environmentID)
		if err != nil {
			return nil, false, fmt.Errorf("deploy: mark environment releasing: %w", err)
		}
		if rows == 0 {
			return nil, false, ErrEnvironmentNotFound
		}
	} else {
		rows, err := s.st.MarkEnvironmentDown(ctx, environmentID)
		if err != nil {
			return nil, false, fmt.Errorf("deploy: mark environment down: %w", err)
		}
		if rows == 0 {
			// The guarded update only misses when the environment is
			// already releasing (the row itself was checked above).
			return nil, false, ErrEnvironmentReleasing
		}
	}

	run, err = jr.BeginRun(ctx, journal.RunInput{
		Kind:          kind,
		ProjectID:     env.ProjectID,
		EnvironmentID: env.ID,
		Actor:         actor,
	})
	if errors.Is(err, journal.ErrRunConflict) {
		return nil, false, ErrDeploymentInFlight
	}
	if err != nil {
		return nil, false, err
	}
	if s.enqueuer != nil {
		s.enqueuer.Enqueue(environmentID)
	}
	return run, false, nil
}
