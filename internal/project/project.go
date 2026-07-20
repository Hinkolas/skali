// Package project owns the definition plane of the product domain: projects,
// their environments, and the versioned draft document each project carries.
// Definition versions are immutable and content-addressed; the draft is the
// only mutable pointer, guarded by an optimistic version counter.
package project

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Hinkolas/skali/internal/store"
)

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

type Service struct {
	st *store.Store
}

func New(st *store.Store) *Service {
	return &Service{st: st}
}

func (s *Service) Create(ctx context.Context, name, displayName string) (*store.Project, error) {
	if !namePattern.MatchString(name) {
		return nil, ErrInvalidName
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("project: generate id: %w", err)
	}
	proj, err := s.st.CreateProject(ctx, store.CreateProjectParams{
		ID:          id,
		Name:        name,
		DisplayName: displayName,
		SourceMode:  "managed",
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
			return nil, ErrProjectNameTaken
		}
		return nil, fmt.Errorf("project: create: %w", err)
	}
	return &proj, nil
}

func (s *Service) List(ctx context.Context) ([]store.Project, error) {
	projects, err := s.st.ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("project: list: %w", err)
	}
	return projects, nil
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (*store.Project, error) {
	proj, err := s.st.GetProjectByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrProjectNotFound
		}
		return nil, fmt.Errorf("project: get: %w", err)
	}
	return &proj, nil
}

// Update applies the optional display-name and source-mode changes. Switching
// the source mode is an explicit product action, never a side effect.
func (s *Service) Update(ctx context.Context, id uuid.UUID, displayName, sourceMode *string) (*store.Project, error) {
	current, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	next := store.UpdateProjectParams{
		ID:          id,
		DisplayName: current.DisplayName,
		SourceMode:  current.SourceMode,
	}
	if displayName != nil {
		next.DisplayName = *displayName
	}
	if sourceMode != nil {
		if *sourceMode != "managed" && *sourceMode != "file" {
			return nil, ErrInvalidSourceMode
		}
		next.SourceMode = *sourceMode
	}
	proj, err := s.st.UpdateProject(ctx, next)
	if err != nil {
		return nil, fmt.Errorf("project: update: %w", err)
	}
	return &proj, nil
}

func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	rows, err := s.st.DeleteProjectByID(ctx, id)
	if err != nil {
		return fmt.Errorf("project: delete: %w", err)
	}
	if rows == 0 {
		return ErrProjectNotFound
	}
	return nil
}

func (s *Service) CreateEnvironment(ctx context.Context, projectID uuid.UUID, name string) (*store.Environment, error) {
	if !namePattern.MatchString(name) {
		return nil, ErrInvalidName
	}
	if _, err := s.Get(ctx, projectID); err != nil {
		return nil, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("project: generate id: %w", err)
	}
	var env store.Environment
	err = s.st.WithTx(ctx, func(q *store.Queries) error {
		var err error
		env, err = q.CreateEnvironment(ctx, store.CreateEnvironmentParams{
			ID:        id,
			ProjectID: projectID,
			Name:      name,
		})
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
				return ErrEnvironmentNameTaken
			}
			return fmt.Errorf("project: create environment: %w", err)
		}
		// The pointer row exists for the environment's whole life; only
		// deploy promotion and rollback ever set its target.
		if err := q.CreateEnvironmentTarget(ctx, env.ID); err != nil {
			return fmt.Errorf("project: create environment target: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &env, nil
}

func (s *Service) ListEnvironments(ctx context.Context, projectID uuid.UUID) ([]store.Environment, error) {
	if _, err := s.Get(ctx, projectID); err != nil {
		return nil, err
	}
	environments, err := s.st.ListEnvironments(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("project: list environments: %w", err)
	}
	return environments, nil
}

func (s *Service) GetEnvironment(ctx context.Context, id uuid.UUID) (*store.Environment, error) {
	env, err := s.st.GetEnvironmentByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrEnvironmentNotFound
		}
		return nil, fmt.Errorf("project: get environment: %w", err)
	}
	return &env, nil
}

func (s *Service) DeleteEnvironment(ctx context.Context, id uuid.UUID) error {
	rows, err := s.st.DeleteEnvironmentByID(ctx, id)
	if err != nil {
		return fmt.Errorf("project: delete environment: %w", err)
	}
	if rows == 0 {
		return ErrEnvironmentNotFound
	}
	return nil
}
