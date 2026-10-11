package deploy

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/store"
)

// ErrRestoreIncomplete: a restore stopped the environment and did not
// complete, so its data may be partial (RestoreIncompleteError names the
// restore).
var ErrRestoreIncomplete = errors.New("deploy: an incomplete restore holds the environment")

// ErrRestoreHoldLost: a restore went to resume an environment it no longer
// holds, or its run no longer runs: someone accepted its data, another
// restore took the hold over, or the run was cancelled (a teardown cancels
// it before it takes the environment down).
var ErrRestoreHoldLost = errors.New("deploy: the restore no longer holds the environment or its run was cancelled")

// RestoreIncompleteError refuses bringing back an environment an incomplete
// restore holds. Only a restore that completes, or an explicit acceptance
// of the data as it is, lifts the hold; a deployment or rollback never
// does.
type RestoreIncompleteError struct {
	RestoreID uuid.UUID
}

func (e *RestoreIncompleteError) Error() string {
	return fmt.Sprintf("deploy: restore %s did not complete and holds the environment", e.RestoreID)
}

func (e *RestoreIncompleteError) Is(target error) bool { return target == ErrRestoreIncomplete }

// restoreHold reports the hold an incomplete restore keeps on the target,
// if any.
func restoreHold(target store.EnvironmentTarget) error {
	if target.IncompleteRestoreID == nil {
		return nil
	}
	return &RestoreIncompleteError{RestoreID: *target.IncompleteRestoreID}
}

// checkRestoreHold refuses a deployment of an environment an incomplete
// restore holds before any work starts; the promotion's own guard backs it
// against a restore that stops the environment in between.
func (s *Service) checkRestoreHold(ctx context.Context, environmentID uuid.UUID) error {
	target, err := s.st.GetEnvironmentTarget(ctx, environmentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("deploy: get target: %w", err)
	}
	return restoreHold(target)
}

// unmovedTarget explains a target write that matched no row: the
// environment is gone or releasing, or an incomplete restore holds it.
func unmovedTarget(ctx context.Context, q *store.Queries, environmentID uuid.UUID) error {
	target, err := q.GetEnvironmentTarget(ctx, environmentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrEnvironmentNotFound
	}
	if err != nil {
		return fmt.Errorf("deploy: get target: %w", err)
	}
	if err := restoreHold(target); err != nil {
		return err
	}
	return ErrEnvironmentNotFound
}
