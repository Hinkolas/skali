package dbstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/utils"
)

// TenantInput records a provisioned logical database. CredentialSecret is
// the NAME of the credential Secret in skali-platform; the value never
// passes through this package.
type TenantInput struct {
	ClaimID      uuid.UUID
	ClusterID    uuid.UUID
	DatabaseName string
	// RoleName is the owner role: it owns the database and never changes.
	RoleName string
	// LoginRole is the role consumers log in as; it defaults to the owner
	// and diverges from it at the first credential rotation.
	LoginRole        string
	CredentialSecret string
	Host             string
	Port             int
}

// RecordTenant durably records the tenant identity for a claim. It is
// idempotent per claim: an existing live tenant is returned as-is, because
// generated identities must never change once recorded.
func (s *Service) RecordTenant(ctx context.Context, in TenantInput) (*store.DatabaseTenant, error) {
	if in.LoginRole == "" {
		in.LoginRole = in.RoleName
	}
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
			LoginRole:        in.LoginRole,
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

// EnvironmentTenants returns the live tenant of every live service claim of
// the environment, each with its claim's service key and phase.
func (s *Service) EnvironmentTenants(ctx context.Context, environmentID uuid.UUID) ([]store.ListLiveDatabaseTenantsByEnvironmentRow, error) {
	rows, err := s.st.ListLiveDatabaseTenantsByEnvironment(ctx, utils.NilWhenZero(environmentID))
	if err != nil {
		return nil, fmt.Errorf("dbstore: list environment tenants: %w", err)
	}
	return rows, nil
}

// ListClusterTenants returns the live tenants on a cluster.
func (s *Service) ListClusterTenants(ctx context.Context, clusterID uuid.UUID) ([]store.DatabaseTenant, error) {
	rows, err := s.st.ListLiveDatabaseTenantsByCluster(ctx, clusterID)
	if err != nil {
		return nil, fmt.Errorf("dbstore: list cluster tenants: %w", err)
	}
	return rows, nil
}

// ListManagedClusterTenants returns the live tenants on a cluster whose
// roles the pool's Cluster spec lists: those not on a claim being torn
// down, since teardown drops the roles itself.
func (s *Service) ListManagedClusterTenants(ctx context.Context, clusterID uuid.UUID) ([]store.DatabaseTenant, error) {
	rows, err := s.st.ListManagedDatabaseTenantsByCluster(ctx, clusterID)
	if err != nil {
		return nil, fmt.Errorf("dbstore: list managed cluster tenants: %w", err)
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

// SetTenantPendingCredential commits the next login role and its Secret
// name with the instant the current one retires. It reports whether the
// row took it: false when the tenant is released or a rotation is already
// pending or retiring (one at a time).
func (s *Service) SetTenantPendingCredential(ctx context.Context, tenantID uuid.UUID, loginRole, credentialSecret string, retireAt time.Time) (bool, error) {
	rows, err := s.st.SetDatabaseTenantPendingCredential(ctx, store.SetDatabaseTenantPendingCredentialParams{
		ID:                      tenantID,
		PendingLoginRole:        &loginRole,
		PendingCredentialSecret: &credentialSecret,
		CredentialRetireAt:      &retireAt,
	})
	if err != nil {
		return false, fmt.Errorf("dbstore: set pending credential: %w", err)
	}
	return rows > 0, nil
}

// BeginTenantCredentialRotation takes the pending login role: it becomes
// current, the current one previous, and the credential version advances
// (the consumer-visible signal). Exactly once per pending role; it reports
// whether this call did it.
func (s *Service) BeginTenantCredentialRotation(ctx context.Context, tenantID uuid.UUID) (bool, error) {
	rows, err := s.st.BeginDatabaseTenantCredentialRotation(ctx, tenantID)
	if err != nil {
		return false, fmt.Errorf("dbstore: begin credential rotation: %w", err)
	}
	return rows > 0, nil
}

// RetireTenantCredentials moves the previous login role's retirement to
// now; a no-op without a previous role.
func (s *Service) RetireTenantCredentials(ctx context.Context, tenantID uuid.UUID) error {
	if _, err := s.st.RetireDatabaseTenantCredentials(ctx, tenantID); err != nil {
		return fmt.Errorf("dbstore: retire credentials: %w", err)
	}
	return nil
}

// FinishTenantCredentialRotation clears the previous login role once it
// is gone from the pool; idempotent.
func (s *Service) FinishTenantCredentialRotation(ctx context.Context, tenantID uuid.UUID) error {
	if _, err := s.st.FinishDatabaseTenantCredentialRotation(ctx, tenantID); err != nil {
		return fmt.Errorf("dbstore: finish credential rotation: %w", err)
	}
	return nil
}

// ListClusterTenantDetails returns the live tenants on a cluster joined
// with their claim, owning project and environment, and the newest
// database storage sample since the cutoff: the pool page's database list.
func (s *Service) ListClusterTenantDetails(ctx context.Context, clusterID uuid.UUID, since time.Time) ([]store.ListLiveDatabaseTenantDetailsByClusterRow, error) {
	rows, err := s.st.ListLiveDatabaseTenantDetailsByCluster(ctx, store.ListLiveDatabaseTenantDetailsByClusterParams{
		ClusterID: clusterID,
		Since:     since,
	})
	if err != nil {
		return nil, fmt.Errorf("dbstore: list cluster tenant details: %w", err)
	}
	return rows, nil
}
