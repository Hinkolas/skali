package dbstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/store"
)

// AllocationInput records a generated bucket identity. CredentialSecret is
// the NAME of the credential Secret in skali-platform; the secret access key
// never passes through this package.
type AllocationInput struct {
	ClaimID          uuid.UUID
	StoreID          uuid.UUID
	BucketName       string
	AccessKeyID      string
	CredentialSecret string
	Endpoint         string
	Region           string
}

// RecordAllocation durably records the bucket identity for a claim and binds
// the claim: with one live store per installation the allocation is the
// placement, so a pending claim moves to bound here. It is idempotent per
// claim: an existing live allocation is returned as-is, because generated
// identities must never change once recorded.
func (s *Service) RecordAllocation(ctx context.Context, in AllocationInput) (*store.BucketAllocation, error) {
	var row store.BucketAllocation
	err := s.st.WithTx(ctx, func(q *store.Queries) error {
		claimRow, err := q.GetBucketClaimForUpdate(ctx, in.ClaimID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("dbstore: lock bucket claim: %w", err)
		}
		existing, err := q.GetLiveBucketAllocationByClaim(ctx, in.ClaimID)
		if err == nil {
			row = existing
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("dbstore: live allocation: %w", err)
		}
		phase := claim.Phase(claimRow.Phase)
		if phase != claim.PhaseBound && phase != claim.PhaseProvisioned &&
			!claim.Phases.Can(phase, claim.PhaseBound) {
			return fmt.Errorf("%w: allocate from %s", ErrInvalidTransition, claimRow.Phase)
		}
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("dbstore: generate id: %w", err)
		}
		row, err = q.CreateBucketAllocation(ctx, store.CreateBucketAllocationParams{
			ID:               id,
			ClaimID:          in.ClaimID,
			StoreID:          in.StoreID,
			BucketName:       in.BucketName,
			AccessKeyID:      in.AccessKeyID,
			CredentialSecret: in.CredentialSecret,
			Endpoint:         in.Endpoint,
			Region:           in.Region,
		})
		if err != nil {
			return fmt.Errorf("dbstore: create allocation: %w", err)
		}
		if phase == claim.PhasePending {
			if _, err := s.transitionBucketClaimTx(ctx, q, in.ClaimID, claim.PhaseBound); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// LiveAllocation returns the claim's live allocation, or ErrNotFound.
func (s *Service) LiveAllocation(ctx context.Context, claimID uuid.UUID) (*store.BucketAllocation, error) {
	row, err := s.st.GetLiveBucketAllocationByClaim(ctx, claimID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("dbstore: live allocation: %w", err)
	}
	return &row, nil
}

// ListStoreAllocations returns the live allocations on a store, the input
// for identity rendering and the usage probe.
func (s *Service) ListStoreAllocations(ctx context.Context, storeID uuid.UUID) ([]store.BucketAllocation, error) {
	rows, err := s.st.ListLiveBucketAllocationsByStore(ctx, storeID)
	if err != nil {
		return nil, fmt.Errorf("dbstore: list store allocations: %w", err)
	}
	return rows, nil
}

// CountStoreAllocations counts live allocations on a store.
func (s *Service) CountStoreAllocations(ctx context.Context, storeID uuid.UUID) (int64, error) {
	count, err := s.st.CountLiveBucketAllocationsByStore(ctx, storeID)
	if err != nil {
		return 0, fmt.Errorf("dbstore: count store allocations: %w", err)
	}
	return count, nil
}

// SetAllocationEndpoint republishes the endpoint after the bucket gains,
// changes, or loses its route.
func (s *Service) SetAllocationEndpoint(ctx context.Context, allocationID uuid.UUID, endpoint string) error {
	rows, err := s.st.SetBucketAllocationEndpoint(ctx, store.SetBucketAllocationEndpointParams{
		ID:       allocationID,
		Endpoint: endpoint,
	})
	if err != nil {
		return fmt.Errorf("dbstore: set allocation endpoint: %w", err)
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

// BeginAllocationCredentialRotation commits a rotated keypair the
// credential Secret already holds: the new access key id, the
// consumer-visible version bump, and the instant the previous keypair
// retires, atomically and exactly once per key. It reports whether this
// call committed (false when the row already carries the key, so a
// repeated pass is a no-op). The secret key lives only in the Secret.
func (s *Service) BeginAllocationCredentialRotation(ctx context.Context, allocationID uuid.UUID, accessKeyID string, retireAt time.Time) (bool, error) {
	rows, err := s.st.BeginBucketAllocationCredentialRotation(ctx, store.BeginBucketAllocationCredentialRotationParams{
		ID: allocationID, AccessKeyID: accessKeyID, CredentialRetireAt: &retireAt,
	})
	if err != nil {
		return false, fmt.Errorf("dbstore: begin allocation credential rotation: %w", err)
	}
	return rows > 0, nil
}

// FinishAllocationCredentialRotation clears the overlap once the previous
// keypair is retired; no overlap is not an error.
func (s *Service) FinishAllocationCredentialRotation(ctx context.Context, allocationID uuid.UUID) error {
	if _, err := s.st.FinishBucketAllocationCredentialRotation(ctx, allocationID); err != nil {
		return fmt.Errorf("dbstore: finish allocation credential rotation: %w", err)
	}
	return nil
}

// FenceAllocation raises the restore fence on a live allocation. Already
// fenced is not an error: a re-run restore fences again.
func (s *Service) FenceAllocation(ctx context.Context, allocationID uuid.UUID) error {
	if _, err := s.st.FenceBucketAllocation(ctx, allocationID); err != nil {
		return fmt.Errorf("dbstore: fence allocation: %w", err)
	}
	return nil
}

// UnfenceAllocation lowers the restore fence; not fenced is not an error.
func (s *Service) UnfenceAllocation(ctx context.Context, allocationID uuid.UUID) error {
	if _, err := s.st.UnfenceBucketAllocation(ctx, allocationID); err != nil {
		return fmt.Errorf("dbstore: unfence allocation: %w", err)
	}
	return nil
}

// EnvironmentDown reports whether the environment is taken down (a restore
// in progress, or one that failed and left it down). The substrate lifts a
// bucket fence once its environment is no longer down.
func (s *Service) EnvironmentDown(ctx context.Context, environmentID uuid.UUID) (bool, error) {
	target, err := s.st.GetEnvironmentTarget(ctx, environmentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, ErrNotFound
		}
		return false, fmt.Errorf("dbstore: environment target: %w", err)
	}
	return target.State == "down", nil
}
