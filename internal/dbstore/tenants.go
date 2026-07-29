package dbstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/store"
)

// TenantInput records a provisioned logical database. CredentialSecret is
// the NAME of the credential Secret in skali-platform; the value never
// passes through this package.
type TenantInput struct {
	ClaimID          uuid.UUID
	ClusterID        uuid.UUID
	DatabaseName     string
	RoleName         string
	CredentialSecret string
	Host             string
	Port             int
}

// RecordTenant durably records the tenant identity for a claim. It is
// idempotent per claim: an existing live tenant is returned as-is, because
// generated identities must never change once recorded.
func (s *Service) RecordTenant(ctx context.Context, in TenantInput) (*store.DatabaseTenant, error) {
	var row store.DatabaseTenant
	err := s.st.WithTx(ctx, func(q *store.Queries) error {
		existing, err := q.GetLiveDatabaseTenantByClaim(ctx, in.ClaimID)
		if err == nil {
			row = existing
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("dbstore: live tenant: %w", err)
		}
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("dbstore: generate id: %w", err)
		}
		row, err = q.CreateDatabaseTenant(ctx, store.CreateDatabaseTenantParams{
			ID:               id,
			ClaimID:          in.ClaimID,
			ClusterID:        in.ClusterID,
			DatabaseName:     in.DatabaseName,
			RoleName:         in.RoleName,
			CredentialSecret: in.CredentialSecret,
			Host:             in.Host,
			Port:             int32(in.Port),
		})
		if err != nil {
			return fmt.Errorf("dbstore: create tenant: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// LiveTenant returns the claim's live tenant, or ErrNotFound.
func (s *Service) LiveTenant(ctx context.Context, claimID uuid.UUID) (*store.DatabaseTenant, error) {
	row, err := s.st.GetLiveDatabaseTenantByClaim(ctx, claimID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("dbstore: live tenant: %w", err)
	}
	return &row, nil
}

// ListClusterTenants returns the live tenants on a cluster, the input for
// managed-role rendering.
func (s *Service) ListClusterTenants(ctx context.Context, clusterID uuid.UUID) ([]store.DatabaseTenant, error) {
	rows, err := s.st.ListLiveDatabaseTenantsByCluster(ctx, clusterID)
	if err != nil {
		return nil, fmt.Errorf("dbstore: list cluster tenants: %w", err)
	}
	return rows, nil
}

// CountClusterTenants counts live tenants on a cluster; zero enables pool GC.
func (s *Service) CountClusterTenants(ctx context.Context, clusterID uuid.UUID) (int64, error) {
	count, err := s.st.CountLiveDatabaseTenantsByCluster(ctx, clusterID)
	if err != nil {
		return 0, fmt.Errorf("dbstore: count cluster tenants: %w", err)
	}
	return count, nil
}

// BumpCredentialVersion records a rotation; the new password lives only in
// the credential Secret.
func (s *Service) BumpCredentialVersion(ctx context.Context, tenantID uuid.UUID) error {
	rows, err := s.st.BumpDatabaseTenantCredentialVersion(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("dbstore: bump credential version: %w", err)
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}
