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

// DeleteEnvironment removes a released environment's row; the foreign keys
// cascade every environment-scoped record with it. Only the reconciler
// calls this, and only after the cluster confirms complete absence.
func (s *Service) DeleteEnvironment(ctx context.Context, environmentID uuid.UUID) error {
	if _, err := s.st.DeleteEnvironmentByID(ctx, environmentID); err != nil {
		return fmt.Errorf("deploy: delete environment: %w", err)
	}
	return nil
}

// Teardown persists the destructive decision behind skali dev down and asks
// the kernel to execute it. Down keeps the namespace, volumes, values, and
// history; purge releases the environment completely, ending with the
// namespace and the environment row deleted. The returned run is the
// journaled teardown the caller can attach to.
func (s *Service) Teardown(ctx context.Context, environmentID uuid.UUID, purge bool, jr *journal.Service, actor string) (*store.Run, error) {
	unlock, lockErr := s.st.LockEnvironment(ctx, environmentID)
	if lockErr != nil {
		return nil, lockErr
	}
	defer unlock()
	env, err := s.st.GetEnvironmentByID(ctx, environmentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrEnvironmentNotFound
		}
		return nil, fmt.Errorf("deploy: get environment: %w", err)
	}

	// An open deployment window means a client is still building against
	// this environment; tearing the target down underneath it would turn
	// its completion into a resurrection race. Refuse instead.
	if _, err := s.st.GetPreparingDeploymentForEnvironment(ctx, environmentID); err == nil {
		return nil, ErrDeploymentInFlight
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("deploy: check in-flight deployment: %w", err)
	}

	// A running run (a rollout the kernel is still converging) is cancelled
	// so the partial unique index admits the teardown run. Journal only:
	// the pointers below are what actually stops the rollout.
	if running, err := s.st.GetRunningRunByEnvironment(ctx, &environmentID); err == nil {
		if err := jr.FinishRun(ctx, running.ID, journal.RunCancelled); err != nil &&
			!errors.Is(err, journal.ErrInvalidTransition) {
			return nil, fmt.Errorf("deploy: cancel running run: %w", err)
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("deploy: check running run: %w", err)
	}

	if purge {
		rows, err := s.st.MarkEnvironmentReleasing(ctx, environmentID)
		if err != nil {
			return nil, fmt.Errorf("deploy: mark environment releasing: %w", err)
		}
		if rows == 0 {
			return nil, ErrEnvironmentNotFound
		}
	} else {
		rows, err := s.st.MarkEnvironmentDown(ctx, environmentID)
		if err != nil {
			return nil, fmt.Errorf("deploy: mark environment down: %w", err)
		}
		if rows == 0 {
			// The guarded update only misses when the environment is
			// already releasing (the row itself was checked above).
			return nil, ErrEnvironmentReleasing
		}
	}

	run, err := jr.CreateRun(ctx, journal.RunInput{
		Kind:          "teardown",
		ProjectID:     env.ProjectID,
		EnvironmentID: env.ID,
		Actor:         actor,
	})
	if err != nil {
		return nil, err
	}
	if err := jr.StartRun(ctx, run.ID); err != nil {
		discardUnstartedRun(ctx, jr, run.ID)
		if errors.Is(err, journal.ErrRunConflict) {
			return nil, ErrDeploymentInFlight
		}
		return nil, err
	}
	if s.enqueuer != nil {
		s.enqueuer.Enqueue(environmentID)
	}
	return run, nil
}
