// Package project owns the definition plane of the product domain: projects,
// their environments, and the versioned draft document each project carries.
// Definition versions are immutable and content-addressed; the draft is the
// only mutable pointer, guarded by an optimistic version counter.
package project

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/authz"
	"github.com/Hinkolas/skali/internal/naming"
	"github.com/Hinkolas/skali/internal/store"
)

type Service struct {
	st *store.Store
}

func New(st *store.Store) *Service {
	return &Service{st: st}
}

// Create makes a project and, when creator is set, its first admin member.
// uuid.Nil creates an ownerless project (system and test paths); every API
// path passes the acting user so the creator can manage what they made.
func (s *Service) Create(ctx context.Context, name, displayName string, creator uuid.UUID) (*store.Project, error) {
	if err := naming.CheckKey(name); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidName, err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("project: generate id: %w", err)
	}
	var proj store.Project
	err = s.st.WithTx(ctx, func(q *store.Queries) error {
		var err error
		proj, err = q.CreateProject(ctx, store.CreateProjectParams{
			ID:          id,
			Name:        name,
			DisplayName: displayName,
			SourceMode:  "managed",
		})
		if err != nil {
			if store.IsUniqueViolation(err) {
				return ErrProjectNameTaken
			}
			return fmt.Errorf("project: create: %w", err)
		}
		if creator == uuid.Nil {
			return nil
		}
		if _, err := q.UpsertProjectMember(ctx, store.UpsertProjectMemberParams{
			ProjectID: proj.ID, UserID: creator, Role: authz.Admin.String(),
		}); err != nil {
			return fmt.Errorf("project: add creator as admin: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
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

// Delete removes a project that has no environments left. Environments
// own namespaces on the cluster and are released by the reconciler through
// a purge (skali env rm), which deletes their rows only once the cluster
// confirms absence; refusing here keeps a project delete from cascading
// those rows away underneath running workloads.
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	return s.st.WithTx(ctx, func(q *store.Queries) error {
		environments, err := q.ListEnvironments(ctx, id)
		if err != nil {
			return fmt.Errorf("project: list environments: %w", err)
		}
		if len(environments) > 0 {
			return ErrProjectHasEnvironments
		}
		rows, err := q.DeleteProjectByID(ctx, id)
		if err != nil {
			return fmt.Errorf("project: delete: %w", err)
		}
		if rows == 0 {
			return ErrProjectNotFound
		}
		return nil
	})
}

// EnvironmentOptions shape a new environment. The zero value is an
// ownerless normal-priority environment (system and test paths).
type EnvironmentOptions struct {
	// Creator gets an explicit admin cell when they are a member below
	// project admin, so they manage and delete what they created without a
	// project admin. Admins need no cell; an instance admin without
	// membership cannot hold one (cells hang off the membership row).
	Creator uuid.UUID
	// Priority is normal or high; empty means normal. Whether the caller may
	// ask for high is the API's decision (instance admins only).
	Priority string
}

func (s *Service) CreateEnvironment(ctx context.Context, projectID uuid.UUID, name string, opts EnvironmentOptions) (*store.Environment, error) {
	if err := naming.CheckKey(name); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidName, err)
	}
	priority := opts.Priority
	if priority == "" {
		priority = authz.PriorityNormal
	}
	if !authz.ValidPriority(priority) {
		return nil, fmt.Errorf("%w: priority must be normal or high", ErrInvalidSettings)
	}
	if _, err := s.Get(ctx, projectID); err != nil {
		return nil, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("project: generate id: %w", err)
	}
	// Creation defaults by priority: a normal environment starts open (a
	// shared scratch space); a high one starts read-only for inheriting
	// members so production is never accidentally wide open.
	maxRole := authz.Admin
	if priority == authz.PriorityHigh {
		maxRole = authz.Read
	}
	var env store.Environment
	err = s.st.WithTx(ctx, func(q *store.Queries) error {
		var err error
		env, err = q.CreateEnvironment(ctx, store.CreateEnvironmentParams{
			ID:        id,
			ProjectID: projectID,
			Name:      name,
			MaxRole:   maxRole.String(),
			Priority:  priority,
		})
		if err != nil {
			if store.IsUniqueViolation(err) {
				return ErrEnvironmentNameTaken
			}
			return fmt.Errorf("project: create environment: %w", err)
		}
		// The pointer row exists for the environment's whole life; only
		// deploy promotion and rollback ever set its target.
		if err := q.CreateEnvironmentTarget(ctx, env.ID); err != nil {
			return fmt.Errorf("project: create environment target: %w", err)
		}
		if opts.Creator == uuid.Nil {
			return nil
		}
		member, err := q.GetProjectMember(ctx, store.GetProjectMemberParams{ProjectID: projectID, UserID: opts.Creator})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return fmt.Errorf("project: get creator membership: %w", err)
		}
		if member.Role == authz.Admin.String() {
			return nil
		}
		if _, err := q.UpsertEnvironmentAccess(ctx, store.UpsertEnvironmentAccessParams{
			EnvironmentID: env.ID, ProjectID: projectID, UserID: opts.Creator, Role: authz.Admin.String(),
		}); err != nil {
			return fmt.Errorf("project: grant creator admin cell: %w", err)
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
