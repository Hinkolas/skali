package dbstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/store"
)

// ClusterInput is the desired shape of a new pool. Exactly one of the owner
// fields matters per class: EnvironmentID for environment pools, ClaimID for
// dedicated pools, neither for shared.
type ClusterInput struct {
	Name          string
	Engine        string
	Major         int
	Class         string
	EnvironmentID uuid.UUID
	ClaimID       uuid.UUID
	Instances     int
	StorageBytes  int64
	Image         string
}

// CreateCluster records a new pool in state active. The partial unique
// indexes enforce the packing invariants (one live shared pool per engine and
// major, one environment pool per environment, one dedicated pool per claim).
func (s *Service) CreateCluster(ctx context.Context, in ClusterInput) (*store.DatabaseCluster, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("dbstore: generate id: %w", err)
	}
	row, err := s.st.CreateDatabaseCluster(ctx, store.CreateDatabaseClusterParams{
		ID:            id,
		Name:          in.Name,
		Engine:        in.Engine,
		Major:         int32(in.Major),
		Class:         in.Class,
		EnvironmentID: optionalID(in.EnvironmentID),
		ClaimID:       optionalID(in.ClaimID),
		Instances:     int32(in.Instances),
		StorageBytes:  in.StorageBytes,
		Image:         in.Image,
		State:         StateActive,
	})
	if err != nil {
		return nil, fmt.Errorf("dbstore: create cluster: %w", err)
	}
	return &row, nil
}

// GetCluster returns a cluster by id regardless of state.
func (s *Service) GetCluster(ctx context.Context, id uuid.UUID) (*store.DatabaseCluster, error) {
	row, err := s.st.GetDatabaseCluster(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("dbstore: get cluster: %w", err)
	}
	return &row, nil
}

// LiveSharedCluster returns the live shared pool for (engine, major), or
// ErrNotFound.
func (s *Service) LiveSharedCluster(ctx context.Context, engine string, major int) (*store.DatabaseCluster, error) {
	row, err := s.st.GetLiveSharedDatabaseCluster(ctx, store.GetLiveSharedDatabaseClusterParams{
		Engine: engine,
		Major:  int32(major),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("dbstore: live shared cluster: %w", err)
	}
	return &row, nil
}

// LiveEnvironmentCluster returns the environment's live pool for
// (engine, major), or ErrNotFound.
func (s *Service) LiveEnvironmentCluster(ctx context.Context, engine string, major int, environmentID uuid.UUID) (*store.DatabaseCluster, error) {
	row, err := s.st.GetLiveEnvironmentDatabaseCluster(ctx, store.GetLiveEnvironmentDatabaseClusterParams{
		Engine:        engine,
		Major:         int32(major),
		EnvironmentID: optionalID(environmentID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("dbstore: live environment cluster: %w", err)
	}
	return &row, nil
}

// LiveDedicatedCluster returns the claim's live dedicated pool, or
// ErrNotFound.
func (s *Service) LiveDedicatedCluster(ctx context.Context, claimID uuid.UUID) (*store.DatabaseCluster, error) {
	row, err := s.st.GetLiveDedicatedDatabaseCluster(ctx, optionalID(claimID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("dbstore: live dedicated cluster: %w", err)
	}
	return &row, nil
}

// ListLiveClusters returns every pool that is not released.
func (s *Service) ListLiveClusters(ctx context.Context) ([]store.DatabaseCluster, error) {
	rows, err := s.st.ListLiveDatabaseClusters(ctx)
	if err != nil {
		return nil, fmt.Errorf("dbstore: list live clusters: %w", err)
	}
	return rows, nil
}

// TransitionCluster applies one guarded state change under a row lock and
// returns the updated cluster.
func (s *Service) TransitionCluster(ctx context.Context, id uuid.UUID, to string) (*store.DatabaseCluster, error) {
	var row store.DatabaseCluster
	err := s.st.WithTx(ctx, func(q *store.Queries) error {
		current, err := q.GetDatabaseClusterForUpdate(ctx, id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("dbstore: lock cluster: %w", err)
		}
		if !ClusterStates.Can(current.State, to) {
			return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, current.State, to)
		}
		rows, err := q.SetDatabaseClusterState(ctx, store.SetDatabaseClusterStateParams{
			ID:        id,
			FromState: current.State,
			ToState:   to,
		})
		if err != nil {
			return fmt.Errorf("dbstore: set cluster state: %w", err)
		}
		if rows == 0 {
			return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, current.State, to)
		}
		current.State = to
		row = current
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// SetClusterShape updates the desired instance count and storage envelope.
func (s *Service) SetClusterShape(ctx context.Context, id uuid.UUID, instances int, storageBytes int64) error {
	rows, err := s.st.SetDatabaseClusterShape(ctx, store.SetDatabaseClusterShapeParams{
		ID:           id,
		Instances:    int32(instances),
		StorageBytes: storageBytes,
	})
	if err != nil {
		return fmt.Errorf("dbstore: set cluster shape: %w", err)
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

// ActiveClaimCount reports how many live claims on the cluster belong to an
// active environment (system claims always count); the local-dev hibernation
// input.
func (s *Service) ActiveClaimCount(ctx context.Context, clusterID uuid.UUID) (int64, error) {
	count, err := s.st.CountActiveDatabaseClaimsByCluster(ctx, clusterID)
	if err != nil {
		return 0, fmt.Errorf("dbstore: active claim count: %w", err)
	}
	return count, nil
}

// ListClusterClaims returns the live claims placed on a cluster.
func (s *Service) ListClusterClaims(ctx context.Context, clusterID uuid.UUID) ([]store.DatabaseClaim, error) {
	rows, err := s.st.ListLiveDatabaseClaimsByCluster(ctx, clusterID)
	if err != nil {
		return nil, fmt.Errorf("dbstore: list cluster claims: %w", err)
	}
	return rows, nil
}
