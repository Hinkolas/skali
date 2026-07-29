package dbstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/store"
)

// StoreInput is the desired shape of the physical object store, derived from
// the object-storage node count at creation time.
type StoreInput struct {
	Name               string
	Masters            int
	VolumeServers      int
	Replication        string
	VolumeStorageBytes int64
	Image              string
}

// CreateObjectStore records the single physical store. The partial unique on
// the live name enforces one live store per installation.
func (s *Service) CreateObjectStore(ctx context.Context, in StoreInput) (*store.ObjectStore, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("dbstore: generate id: %w", err)
	}
	row, err := s.st.CreateObjectStore(ctx, store.CreateObjectStoreParams{
		ID:                 id,
		Name:               in.Name,
		Masters:            int32(in.Masters),
		VolumeServers:      int32(in.VolumeServers),
		Replication:        in.Replication,
		VolumeStorageBytes: in.VolumeStorageBytes,
		Image:              in.Image,
		State:              StateActive,
	})
	if err != nil {
		return nil, fmt.Errorf("dbstore: create object store: %w", err)
	}
	return &row, nil
}

// GetObjectStore returns a store by id regardless of state.
func (s *Service) GetObjectStore(ctx context.Context, id uuid.UUID) (*store.ObjectStore, error) {
	row, err := s.st.GetObjectStore(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("dbstore: get object store: %w", err)
	}
	return &row, nil
}

// LiveObjectStore returns the installation's live store, or ErrNotFound.
func (s *Service) LiveObjectStore(ctx context.Context) (*store.ObjectStore, error) {
	row, err := s.st.GetLiveObjectStore(ctx)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("dbstore: live object store: %w", err)
	}
	return &row, nil
}

// TransitionObjectStore applies one guarded state change under a row lock
// and returns the updated store.
func (s *Service) TransitionObjectStore(ctx context.Context, id uuid.UUID, to string) (*store.ObjectStore, error) {
	var row store.ObjectStore
	err := s.st.WithTx(ctx, func(q *store.Queries) error {
		current, err := q.GetObjectStoreForUpdate(ctx, id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("dbstore: lock object store: %w", err)
		}
		if !StoreStates.Can(current.State, to) {
			return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, current.State, to)
		}
		rows, err := q.SetObjectStoreState(ctx, store.SetObjectStoreStateParams{
			ID:        id,
			FromState: current.State,
			ToState:   to,
		})
		if err != nil {
			return fmt.Errorf("dbstore: set object store state: %w", err)
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

// SetObjectStoreShape records desired topology growth. Never changes the
// name or image: those identify the store.
func (s *Service) SetObjectStoreShape(ctx context.Context, id uuid.UUID, in StoreInput) error {
	rows, err := s.st.SetObjectStoreShape(ctx, store.SetObjectStoreShapeParams{
		ID:                 id,
		Masters:            int32(in.Masters),
		VolumeServers:      int32(in.VolumeServers),
		Replication:        in.Replication,
		VolumeStorageBytes: in.VolumeStorageBytes,
	})
	if err != nil {
		return fmt.Errorf("dbstore: set object store shape: %w", err)
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}
