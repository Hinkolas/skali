package dbstore

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/store"
)

// OpenConnectionIncident records that a pool refuses new connections and
// returns the open incident's id. A pool with an incident already open
// keeps it: the identity and start stay those of the first confirmation.
func (s *Service) OpenConnectionIncident(ctx context.Context, clusterID uuid.UUID, code, message string, openedAt time.Time) (uuid.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, err
	}
	row, err := s.st.OpenPoolConnectionIncident(ctx, store.OpenPoolConnectionIncidentParams{
		ID: id, ClusterID: clusterID, Code: code, Message: message, OpenedAt: openedAt,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("dbstore: open connection incident: %w", err)
	}
	return row.ID, nil
}

// CloseConnectionIncident closes the pool's open incident, if any.
func (s *Service) CloseConnectionIncident(ctx context.Context, clusterID uuid.UUID, closedAt time.Time) error {
	if _, err := s.st.ClosePoolConnectionIncident(ctx, store.ClosePoolConnectionIncidentParams{
		ClusterID: clusterID, ClosedAt: &closedAt,
	}); err != nil {
		return fmt.Errorf("dbstore: close connection incident: %w", err)
	}
	return nil
}

// OpenConnectionIncidents lists every pool's open incident.
func (s *Service) OpenConnectionIncidents(ctx context.Context) ([]store.PoolConnectionIncident, error) {
	rows, err := s.st.ListOpenPoolConnectionIncidents(ctx)
	if err != nil {
		return nil, fmt.Errorf("dbstore: list open connection incidents: %w", err)
	}
	return rows, nil
}

// ConnectionIncidents lists a pool's newest incidents, open or closed.
func (s *Service) ConnectionIncidents(ctx context.Context, clusterID uuid.UUID, limit int32) ([]store.PoolConnectionIncident, error) {
	rows, err := s.st.ListPoolConnectionIncidents(ctx, store.ListPoolConnectionIncidentsParams{
		ClusterID: clusterID, Limit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("dbstore: list connection incidents: %w", err)
	}
	return rows, nil
}
