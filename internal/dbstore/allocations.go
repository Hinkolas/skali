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

// SetAllocationEndpoint republishes the endpoint after the installation
// gains or loses an external S3 domain.
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

// BumpAllocationCredentialVersion records a rotation; the new secret key
// lives only in the credential Secret.
func (s *Service) BumpAllocationCredentialVersion(ctx context.Context, allocationID uuid.UUID) error {
	rows, err := s.st.BumpBucketAllocationCredentialVersion(ctx, allocationID)
	if err != nil {
		return fmt.Errorf("dbstore: bump allocation credential version: %w", err)
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}
