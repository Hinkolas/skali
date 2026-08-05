// Package buildstore persists build records for every build regardless of
// where it ran, guarded by the build lifecycle machine. The builds table
// drives builds; the journal only explains them (a build row references its
// run and step key, and journal deletion never changes build state). Local
// builds are records of client-side work: created already running by the
// CLI, kept alive by heartbeats, and never claimable. The queued and
// claimed states plus the lease columns belong to the R4 worker protocol.
package buildstore

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

var (
	ErrNotFound = errors.New("buildstore: not found")
	// ErrInvalidTransition: the requested status change is not permitted by
	// the build lifecycle machine.
	ErrInvalidTransition = errors.New("buildstore: invalid status transition")
)

type Service struct {
	st *store.Store
}

func New(st *store.Store) *Service {
	return &Service{st: st}
}

// Local describes one build the CLI is executing right now.
type Local struct {
	ProjectID    uuid.UUID
	DeploymentID uuid.UUID
	Application  string
	Platform     string
	ContextHash  string
	ConfigHash   string
	ArtifactID   uuid.UUID
	RunID        uuid.UUID
	StepKey      string
}

// CreateLocal records a local build in the running state with a fresh
// heartbeat; the executor is already building when the record exists.
func (s *Service) CreateLocal(ctx context.Context, in Local) (*store.Build, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("buildstore: generate id: %w", err)
	}
	now := time.Now()
	row, err := s.st.CreateBuild(ctx, store.CreateBuildParams{
		ID:           id,
		ProjectID:    utils.NilWhenZero(in.ProjectID),
		DeploymentID: utils.NilWhenZero(in.DeploymentID),
		Application:  in.Application,
		Origin:       "local",
		Status:       string(StatusRunning),
		Platform:     in.Platform,
		ContextHash:  in.ContextHash,
		ConfigHash:   in.ConfigHash,
		ArtifactID:   utils.NilWhenZero(in.ArtifactID),
		RunID:        utils.NilWhenZero(in.RunID),
		StepKey:      in.StepKey,
		StartedAt:    &now,
		HeartbeatAt:  &now,
	})
	if err != nil {
		return nil, fmt.Errorf("buildstore: create local build: %w", err)
	}
	return &row, nil
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (*store.Build, error) {
	row, err := s.st.GetBuildByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("buildstore: get build: %w", err)
	}
	return &row, nil
}

// Finish moves a build to a terminal status under the machine guard.
func (s *Service) Finish(ctx context.Context, id uuid.UUID, to Status) error {
	if !Statuses.Terminal(to) {
		return fmt.Errorf("%w: %s is not terminal", ErrInvalidTransition, to)
	}
	return s.transition(ctx, id, to)
}

// Heartbeat records liveness for a running build; false reports that the
// build is no longer running (finished or swept) so the executor stops.
func (s *Service) Heartbeat(ctx context.Context, id uuid.UUID) (bool, error) {
	rows, err := s.st.HeartbeatBuild(ctx, id)
	if err != nil {
		return false, fmt.Errorf("buildstore: heartbeat: %w", err)
	}
	return rows > 0, nil
}

func (s *Service) ListForDeployment(ctx context.Context, deploymentID uuid.UUID) ([]store.Build, error) {
	deployment := deploymentID
	rows, err := s.st.ListBuildsForDeployment(ctx, &deployment)
	if err != nil {
		return nil, fmt.Errorf("buildstore: list builds: %w", err)
	}
	return rows, nil
}

// ListStaleLocal reports local running builds without a heartbeat since the
// cutoff; the deployment sweeper fails them and their deployments.
func (s *Service) ListStaleLocal(ctx context.Context, cutoff time.Time) ([]store.Build, error) {
	rows, err := s.st.ListStaleLocalBuilds(ctx, &cutoff)
	if err != nil {
		return nil, fmt.Errorf("buildstore: list stale local builds: %w", err)
	}
	return rows, nil
}

// transition applies one guarded status change under a row lock.
func (s *Service) transition(ctx context.Context, id uuid.UUID, to Status) error {
	return s.st.WithTx(ctx, func(q *store.Queries) error {
		row, err := q.GetBuildForUpdate(ctx, id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("buildstore: lock: %w", err)
		}
		if !Statuses.Can(Status(row.Status), to) {
			return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, row.Status, to)
		}
		return q.SetBuildStatus(ctx, store.SetBuildStatusParams{
			ID: id, Status: string(to),
		})
	})
}
