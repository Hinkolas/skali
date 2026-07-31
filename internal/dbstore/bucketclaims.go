package dbstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/store"
)

// EnsureBucketClaim records desired bucket capability for an owner. It
// returns the live claim, creating it in phase pending when absent and
// folding mutable spec drift (quotas, lifecycle windows) into an existing
// one. A change to visibility or versioning is ErrSpecConflict: those change
// the bucket's externally observable contract, and replacing it is a
// destructive decision that belongs to plan/deploy, not to reconciliation.
func (s *Service) EnsureBucketClaim(ctx context.Context, owner Owner, spec BucketSpec) (*store.BucketClaim, error) {
	var row store.BucketClaim
	err := s.st.WithTx(ctx, func(q *store.Queries) error {
		existing, err := s.liveBucketClaim(ctx, q, owner)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			id, err := uuid.NewV7()
			if err != nil {
				return fmt.Errorf("dbstore: generate id: %w", err)
			}
			row, err = q.CreateBucketClaim(ctx, store.CreateBucketClaimParams{
				ID:                           id,
				OwnerKind:                    owner.Kind,
				ProjectID:                    optionalID(owner.ProjectID),
				EnvironmentID:                optionalID(owner.EnvironmentID),
				ServiceKey:                   owner.ServiceKey,
				SystemKey:                    owner.SystemKey,
				OwnerRef:                     owner.Ref,
				Visibility:                   spec.Visibility,
				StorageQuotaBytes:            spec.StorageQuotaBytes,
				ObjectQuota:                  spec.ObjectQuota,
				MaxObjectBytes:               spec.MaxObjectBytes,
				Versioning:                   spec.Versioning,
				AbortUploadsAfterSeconds:     spec.AbortUploadsAfterSeconds,
				ExpireNoncurrentAfterSeconds: spec.ExpireNoncurrentAfterSeconds,
			})
			if err != nil {
				return fmt.Errorf("dbstore: create bucket claim: %w", err)
			}
			return nil
		case err != nil:
			return fmt.Errorf("dbstore: lookup bucket claim: %w", err)
		}

		if existing.Visibility != spec.Visibility || existing.Versioning != spec.Versioning {
			return fmt.Errorf("%w: %s/%s -> %s/%s", ErrSpecConflict,
				existing.Visibility, existing.Versioning,
				spec.Visibility, spec.Versioning)
		}
		if existing.StorageQuotaBytes != spec.StorageQuotaBytes ||
			existing.ObjectQuota != spec.ObjectQuota ||
			existing.MaxObjectBytes != spec.MaxObjectBytes ||
			existing.AbortUploadsAfterSeconds != spec.AbortUploadsAfterSeconds ||
			existing.ExpireNoncurrentAfterSeconds != spec.ExpireNoncurrentAfterSeconds {
			if _, err := q.SetBucketClaimSpec(ctx, store.SetBucketClaimSpecParams{
				ID:                           existing.ID,
				StorageQuotaBytes:            spec.StorageQuotaBytes,
				ObjectQuota:                  spec.ObjectQuota,
				MaxObjectBytes:               spec.MaxObjectBytes,
				AbortUploadsAfterSeconds:     spec.AbortUploadsAfterSeconds,
				ExpireNoncurrentAfterSeconds: spec.ExpireNoncurrentAfterSeconds,
			}); err != nil {
				return fmt.Errorf("dbstore: update bucket claim spec: %w", err)
			}
			existing, err = q.GetBucketClaim(ctx, existing.ID)
			if err != nil {
				return fmt.Errorf("dbstore: reload bucket claim: %w", err)
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

func (s *Service) liveBucketClaim(ctx context.Context, q *store.Queries, owner Owner) (store.BucketClaim, error) {
	if owner.Kind == OwnerSystem {
		return q.GetLiveSystemBucketClaim(ctx, owner.SystemKey)
	}
	return q.GetLiveServiceBucketClaim(ctx, store.GetLiveServiceBucketClaimParams{
		EnvironmentID: optionalID(owner.EnvironmentID),
		ServiceKey:    owner.ServiceKey,
	})
}

// GetBucketClaim returns a claim by id regardless of phase.
func (s *Service) GetBucketClaim(ctx context.Context, id uuid.UUID) (*store.BucketClaim, error) {
	row, err := s.st.GetBucketClaim(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("dbstore: get bucket claim: %w", err)
	}
	return &row, nil
}

// LiveServiceBucketClaim returns the live claim for a service owner, or
// ErrNotFound.
func (s *Service) LiveServiceBucketClaim(ctx context.Context, environmentID uuid.UUID, serviceKey string) (*store.BucketClaim, error) {
	row, err := s.st.GetLiveServiceBucketClaim(ctx, store.GetLiveServiceBucketClaimParams{
		EnvironmentID: optionalID(environmentID),
		ServiceKey:    serviceKey,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("dbstore: live service bucket claim: %w", err)
	}
	return &row, nil
}

// ListEnvironmentBucketClaims returns the environment's live service claims.
func (s *Service) ListEnvironmentBucketClaims(ctx context.Context, environmentID uuid.UUID) ([]store.BucketClaim, error) {
	rows, err := s.st.ListLiveBucketClaimsByEnvironment(ctx, optionalID(environmentID))
	if err != nil {
		return nil, fmt.Errorf("dbstore: list environment bucket claims: %w", err)
	}
	return rows, nil
}

// ListLiveBucketClaims returns every claim that is not released, for
// rebuilding observed projections on boot.
func (s *Service) ListLiveBucketClaims(ctx context.Context) ([]store.BucketClaim, error) {
	rows, err := s.st.ListLiveBucketClaims(ctx)
	if err != nil {
		return nil, fmt.Errorf("dbstore: list live bucket claims: %w", err)
	}
	return rows, nil
}

// ListUnsettledBucketClaims returns every claim with reconciliation work
// outstanding; the substrate boot pass enqueues these.
func (s *Service) ListUnsettledBucketClaims(ctx context.Context) ([]store.BucketClaim, error) {
	rows, err := s.st.ListUnsettledBucketClaims(ctx)
	if err != nil {
		return nil, fmt.Errorf("dbstore: list unsettled bucket claims: %w", err)
	}
	return rows, nil
}

// ListStoreBucketClaims returns the live claims allocated on a store, for
// identity rendering and GC checks.
func (s *Service) ListStoreBucketClaims(ctx context.Context, storeID uuid.UUID) ([]store.BucketClaim, error) {
	rows, err := s.st.ListLiveBucketClaimsByStore(ctx, storeID)
	if err != nil {
		return nil, fmt.Errorf("dbstore: list store bucket claims: %w", err)
	}
	return rows, nil
}

// TransitionBucketClaim applies one guarded phase change under a row lock
// and returns the updated claim.
func (s *Service) TransitionBucketClaim(ctx context.Context, id uuid.UUID, to claim.Phase) (*store.BucketClaim, error) {
	var row store.BucketClaim
	err := s.st.WithTx(ctx, func(q *store.Queries) error {
		var err error
		row, err = s.transitionBucketClaimTx(ctx, q, id, to)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (s *Service) transitionBucketClaimTx(ctx context.Context, q *store.Queries, id uuid.UUID, to claim.Phase) (store.BucketClaim, error) {
	row, err := q.GetBucketClaimForUpdate(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.BucketClaim{}, ErrNotFound
		}
		return store.BucketClaim{}, fmt.Errorf("dbstore: lock bucket claim: %w", err)
	}
	if !claim.Phases.Can(claim.Phase(row.Phase), to) {
		return store.BucketClaim{}, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, row.Phase, to)
	}
	rows, err := q.SetBucketClaimPhase(ctx, store.SetBucketClaimPhaseParams{
		ID:        id,
		FromPhase: row.Phase,
		ToPhase:   string(to),
	})
	if err != nil {
		return store.BucketClaim{}, fmt.Errorf("dbstore: set bucket claim phase: %w", err)
	}
	if rows == 0 {
		return store.BucketClaim{}, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, row.Phase, to)
	}
	row.Phase = string(to)
	return row, nil
}

// ReleaseBucketClaim persists the destructive decision. A claim that was
// never allocated releases directly; an allocated or provisioned claim
// enters releasing and CompleteBucketClaimRelease finishes after teardown.
// The returned claim carries the resulting phase.
func (s *Service) ReleaseBucketClaim(ctx context.Context, id uuid.UUID) (*store.BucketClaim, error) {
	var row store.BucketClaim
	err := s.st.WithTx(ctx, func(q *store.Queries) error {
		current, err := q.GetBucketClaimForUpdate(ctx, id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("dbstore: lock bucket claim: %w", err)
		}
		switch claim.Phase(current.Phase) {
		case claim.PhaseReleasing, claim.PhaseReleased:
			row = current
			return nil
		case claim.PhasePending:
			row, err = s.transitionBucketClaimTx(ctx, q, id, claim.PhaseReleased)
			return err
		default:
			row, err = s.transitionBucketClaimTx(ctx, q, id, claim.PhaseReleasing)
			return err
		}
	})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// CompleteBucketClaimRelease finishes teardown: the live allocation is
// released and the claim moves from releasing to released.
func (s *Service) CompleteBucketClaimRelease(ctx context.Context, id uuid.UUID) error {
	return s.st.WithTx(ctx, func(q *store.Queries) error {
		allocation, err := q.GetLiveBucketAllocationByClaim(ctx, id)
		if err == nil {
			if _, err := q.ReleaseBucketAllocation(ctx, allocation.ID); err != nil {
				return fmt.Errorf("dbstore: release allocation: %w", err)
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("dbstore: live allocation: %w", err)
		}
		_, err = s.transitionBucketClaimTx(ctx, q, id, claim.PhaseReleased)
		return err
	})
}
