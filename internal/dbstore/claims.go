package dbstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/utils"
)

// EnsureClaim records desired database capability for an owner. It returns
// the live claim, creating it in phase pending when absent and folding
// mutable spec drift (availability, storage, extensions, PITR) into an
// existing one. A change to engine, major, or isolation is ErrSpecConflict:
// those select the physical home, and replacing it is a destructive decision
// that belongs to plan/deploy, not to reconciliation.
func (s *Service) EnsureClaim(ctx context.Context, owner Owner, spec ClaimSpec) (*store.DatabaseClaim, error) {
	var row store.DatabaseClaim
	err := s.st.WithTx(ctx, func(q *store.Queries) error {
		existing, err := s.liveClaim(ctx, q, owner)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			id, err := uuid.NewV7()
			if err != nil {
				return fmt.Errorf("dbstore: generate id: %w", err)
			}
			row, err = q.CreateDatabaseClaim(ctx, store.CreateDatabaseClaimParams{
				ID:            id,
				OwnerKind:     owner.Kind,
				ProjectID:     utils.NilWhenZero(owner.ProjectID),
				EnvironmentID: utils.NilWhenZero(owner.EnvironmentID),
				ServiceKey:    owner.ServiceKey,
				SystemKey:     owner.SystemKey,
				OwnerRef:      owner.Ref,
				Engine:        spec.Engine,
				Major:         int32(spec.Major),
				Isolation:     spec.Isolation,
				Availability:  spec.Availability,
				StorageBytes:  spec.StorageBytes,
				Extensions:    marshalExtensions(spec.Extensions),
				PitrSeconds:   spec.PITRSeconds,
			})
			if err != nil {
				return fmt.Errorf("dbstore: create claim: %w", err)
			}
			return nil
		case err != nil:
			return fmt.Errorf("dbstore: lookup claim: %w", err)
		}

		if existing.Engine != spec.Engine || existing.Major != int32(spec.Major) ||
			existing.Isolation != spec.Isolation {
			return fmt.Errorf("%w: %s/%d/%s -> %s/%d/%s", ErrSpecConflict,
				existing.Engine, existing.Major, existing.Isolation,
				spec.Engine, spec.Major, spec.Isolation)
		}
		extensions := marshalExtensions(spec.Extensions)
		if existing.Availability != spec.Availability ||
			existing.StorageBytes != spec.StorageBytes ||
			existing.PitrSeconds != spec.PITRSeconds ||
			!bytes.Equal(existing.Extensions, extensions) {
			if _, err := q.SetDatabaseClaimSpec(ctx, store.SetDatabaseClaimSpecParams{
				ID:           existing.ID,
				Availability: spec.Availability,
				StorageBytes: spec.StorageBytes,
				Extensions:   extensions,
				PitrSeconds:  spec.PITRSeconds,
			}); err != nil {
				return fmt.Errorf("dbstore: update claim spec: %w", err)
			}
			existing, err = q.GetDatabaseClaim(ctx, existing.ID)
			if err != nil {
				return fmt.Errorf("dbstore: reload claim: %w", err)
			}
		}
		row = existing
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (s *Service) liveClaim(ctx context.Context, q *store.Queries, owner Owner) (store.DatabaseClaim, error) {
	if owner.Kind == OwnerSystem {
		return q.GetLiveSystemDatabaseClaim(ctx, owner.SystemKey)
	}
	return q.GetLiveServiceDatabaseClaim(ctx, store.GetLiveServiceDatabaseClaimParams{
		EnvironmentID: utils.NilWhenZero(owner.EnvironmentID),
		ServiceKey:    owner.ServiceKey,
	})
}

// GetClaim returns a claim by id regardless of phase.
func (s *Service) GetClaim(ctx context.Context, id uuid.UUID) (*store.DatabaseClaim, error) {
	row, err := s.st.GetDatabaseClaim(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("dbstore: get claim: %w", err)
	}
	return &row, nil
}

// LiveServiceClaim returns the live claim for a service owner, or
// ErrNotFound.
func (s *Service) LiveServiceClaim(ctx context.Context, environmentID uuid.UUID, serviceKey string) (*store.DatabaseClaim, error) {
	row, err := s.st.GetLiveServiceDatabaseClaim(ctx, store.GetLiveServiceDatabaseClaimParams{
		EnvironmentID: utils.NilWhenZero(environmentID),
		ServiceKey:    serviceKey,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("dbstore: live service claim: %w", err)
	}
	return &row, nil
}

// LiveSystemClaim returns the live claim for a system owner, or ErrNotFound.
func (s *Service) LiveSystemClaim(ctx context.Context, systemKey string) (*store.DatabaseClaim, error) {
	row, err := s.st.GetLiveSystemDatabaseClaim(ctx, systemKey)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("dbstore: live system claim: %w", err)
	}
	return &row, nil
}

// ListEnvironmentClaims returns the environment's live service claims.
func (s *Service) ListEnvironmentClaims(ctx context.Context, environmentID uuid.UUID) ([]store.DatabaseClaim, error) {
	rows, err := s.st.ListLiveDatabaseClaimsByEnvironment(ctx, utils.NilWhenZero(environmentID))
	if err != nil {
		return nil, fmt.Errorf("dbstore: list environment claims: %w", err)
	}
	return rows, nil
}

// ListLiveClaims returns every claim that is not released, for rebuilding
// observed projections on boot.
func (s *Service) ListLiveClaims(ctx context.Context) ([]store.DatabaseClaim, error) {
	rows, err := s.st.ListLiveDatabaseClaims(ctx)
	if err != nil {
		return nil, fmt.Errorf("dbstore: list live claims: %w", err)
	}
	return rows, nil
}

// ListUnsettledClaims returns every claim with reconciliation work
// outstanding; the substrate boot pass enqueues these.
func (s *Service) ListUnsettledClaims(ctx context.Context) ([]store.DatabaseClaim, error) {
	rows, err := s.st.ListUnsettledDatabaseClaims(ctx)
	if err != nil {
		return nil, fmt.Errorf("dbstore: list unsettled claims: %w", err)
	}
	return rows, nil
}

// TransitionClaim applies one guarded phase change under a row lock and
// returns the updated claim.
func (s *Service) TransitionClaim(ctx context.Context, id uuid.UUID, to claim.Phase) (*store.DatabaseClaim, error) {
	var row store.DatabaseClaim
	err := s.st.WithTx(ctx, func(q *store.Queries) error {
		var err error
		row, err = s.transitionClaimTx(ctx, q, id, to)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (s *Service) transitionClaimTx(ctx context.Context, q *store.Queries, id uuid.UUID, to claim.Phase) (store.DatabaseClaim, error) {
	row, err := q.GetDatabaseClaimForUpdate(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.DatabaseClaim{}, ErrNotFound
		}
		return store.DatabaseClaim{}, fmt.Errorf("dbstore: lock claim: %w", err)
	}
	if !claim.Phases.Can(claim.Phase(row.Phase), to) {
		return store.DatabaseClaim{}, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, row.Phase, to)
	}
	rows, err := q.SetDatabaseClaimPhase(ctx, store.SetDatabaseClaimPhaseParams{
		ID:        id,
		FromPhase: row.Phase,
		ToPhase:   string(to),
	})
	if err != nil {
		return store.DatabaseClaim{}, fmt.Errorf("dbstore: set claim phase: %w", err)
	}
	if rows == 0 {
		return store.DatabaseClaim{}, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, row.Phase, to)
	}
	row.Phase = string(to)
	return row, nil
}

// BindClaim places a claim onto a cluster and moves it to bound. Re-binding
// to the same cluster is idempotent; binding a bound or provisioned claim to
// a different cluster supersedes the placement (relocation) without leaving
// the phase.
func (s *Service) BindClaim(ctx context.Context, claimID, clusterID uuid.UUID) (*store.DatabasePlacement, error) {
	var placement store.DatabasePlacement
	err := s.st.WithTx(ctx, func(q *store.Queries) error {
		row, err := q.GetDatabaseClaimForUpdate(ctx, claimID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("dbstore: lock claim: %w", err)
		}
		phase := claim.Phase(row.Phase)
		if phase != claim.PhaseBound && phase != claim.PhaseProvisioned &&
			!claim.Phases.Can(phase, claim.PhaseBound) {
			return fmt.Errorf("%w: bind from %s", ErrInvalidTransition, row.Phase)
		}
		active, err := q.GetActiveDatabasePlacement(ctx, claimID)
		if err == nil && active.ClusterID == clusterID {
			placement = active
			return nil
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("dbstore: active placement: %w", err)
		}
		if _, err := q.SupersedeDatabasePlacement(ctx, claimID); err != nil {
			return fmt.Errorf("dbstore: supersede placement: %w", err)
		}
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("dbstore: generate id: %w", err)
		}
		placement, err = q.CreateDatabasePlacement(ctx, store.CreateDatabasePlacementParams{
			ID:        id,
			ClaimID:   claimID,
			ClusterID: clusterID,
		})
		if err != nil {
			return fmt.Errorf("dbstore: create placement: %w", err)
		}
		if phase == claim.PhasePending {
			if _, err := s.transitionClaimTx(ctx, q, claimID, claim.PhaseBound); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &placement, nil
}

// UnbindClaim revokes the placement of a claim that has not been provisioned,
// returning it to pending.
func (s *Service) UnbindClaim(ctx context.Context, claimID uuid.UUID) error {
	return s.st.WithTx(ctx, func(q *store.Queries) error {
		if _, err := s.transitionClaimTx(ctx, q, claimID, claim.PhasePending); err != nil {
			return err
		}
		if _, err := q.SupersedeDatabasePlacement(ctx, claimID); err != nil {
			return fmt.Errorf("dbstore: supersede placement: %w", err)
		}
		return nil
	})
}

// ActivePlacement returns the claim's active placement, or ErrNotFound.
func (s *Service) ActivePlacement(ctx context.Context, claimID uuid.UUID) (*store.DatabasePlacement, error) {
	row, err := s.st.GetActiveDatabasePlacement(ctx, claimID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("dbstore: active placement: %w", err)
	}
	return &row, nil
}

// ReleaseClaim persists the destructive decision. A claim that was never
// placed releases directly; a placed or provisioned claim enters releasing
// and CompleteClaimRelease finishes after teardown. The returned claim
// carries the resulting phase.
func (s *Service) ReleaseClaim(ctx context.Context, id uuid.UUID) (*store.DatabaseClaim, error) {
	var row store.DatabaseClaim
	err := s.st.WithTx(ctx, func(q *store.Queries) error {
		current, err := q.GetDatabaseClaimForUpdate(ctx, id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("dbstore: lock claim: %w", err)
		}
		switch claim.Phase(current.Phase) {
		case claim.PhaseReleasing, claim.PhaseReleased:
			row = current
			return nil
		case claim.PhasePending:
			if _, err := q.SupersedeDatabasePlacement(ctx, id); err != nil {
				return fmt.Errorf("dbstore: supersede placement: %w", err)
			}
			row, err = s.transitionClaimTx(ctx, q, id, claim.PhaseReleased)
			return err
		default:
			row, err = s.transitionClaimTx(ctx, q, id, claim.PhaseReleasing)
			return err
		}
	})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// CompleteClaimRelease finishes teardown: the live tenant is released, the
// placement superseded, and the claim moves from releasing to released.
func (s *Service) CompleteClaimRelease(ctx context.Context, id uuid.UUID) error {
	return s.st.WithTx(ctx, func(q *store.Queries) error {
		tenant, err := q.GetLiveDatabaseTenantByClaim(ctx, id)
		if err == nil {
			if _, err := q.ReleaseDatabaseTenant(ctx, tenant.ID); err != nil {
				return fmt.Errorf("dbstore: release tenant: %w", err)
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("dbstore: live tenant: %w", err)
		}
		if _, err := q.SupersedeDatabasePlacement(ctx, id); err != nil {
			return fmt.Errorf("dbstore: supersede placement: %w", err)
		}
		_, err = s.transitionClaimTx(ctx, q, id, claim.PhaseReleased)
		return err
	})
}
