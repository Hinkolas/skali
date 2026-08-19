package project

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Hinkolas/skali/internal/authz"
	"github.com/Hinkolas/skali/internal/store"
)

// Membership and cells: who holds which role where. The meaning of a role
// lives in internal/authz; this file only stores and validates rows.

// Member is one project membership with the user's display columns.
type Member struct {
	UserID uuid.UUID
	Email  string
	Name   string
	Role   authz.Role
}

// Cell is one per-environment override with the user's display columns.
type Cell struct {
	EnvironmentID uuid.UUID
	UserID        uuid.UUID
	Email         string
	Name          string
	Role          authz.Role
}

func (s *Service) ListMembers(ctx context.Context, projectID uuid.UUID) ([]Member, error) {
	if _, err := s.Get(ctx, projectID); err != nil {
		return nil, err
	}
	rows, err := s.st.ListProjectMembers(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("project: list members: %w", err)
	}
	members := make([]Member, 0, len(rows))
	for _, row := range rows {
		role, ok := authz.ParseRole(row.Role)
		if !ok {
			return nil, fmt.Errorf("project: unknown stored role %q", row.Role)
		}
		members = append(members, Member{UserID: row.UserID, Email: row.Email, Name: row.Name, Role: role})
	}
	return members, nil
}

// SetMember adds or changes one membership.
func (s *Service) SetMember(ctx context.Context, projectID, userID uuid.UUID, role authz.Role) (*Member, error) {
	if !authz.ValidProjectRole(role.String()) {
		return nil, fmt.Errorf("%w: project roles are %s", ErrInvalidRole, authz.ProjectRoleNames())
	}
	if _, err := s.Get(ctx, projectID); err != nil {
		return nil, err
	}
	user, err := s.st.GetUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("project: get user: %w", err)
	}
	if _, err := s.st.UpsertProjectMember(ctx, store.UpsertProjectMemberParams{
		ProjectID: projectID, UserID: userID, Role: role.String(),
	}); err != nil {
		return nil, fmt.Errorf("project: set member: %w", err)
	}
	return &Member{UserID: user.ID, Email: user.Email, Name: user.Name, Role: role}, nil
}

// RemoveMember drops the membership; the user's cells on the project's
// environments go with it (foreign key cascade).
func (s *Service) RemoveMember(ctx context.Context, projectID, userID uuid.UUID) error {
	rows, err := s.st.DeleteProjectMember(ctx, store.DeleteProjectMemberParams{ProjectID: projectID, UserID: userID})
	if err != nil {
		return fmt.Errorf("project: remove member: %w", err)
	}
	if rows == 0 {
		return ErrNotMember
	}
	return nil
}

func (s *Service) ListEnvironmentAccess(ctx context.Context, environmentID uuid.UUID) ([]Cell, error) {
	if _, err := s.GetEnvironment(ctx, environmentID); err != nil {
		return nil, err
	}
	rows, err := s.st.ListEnvironmentAccess(ctx, environmentID)
	if err != nil {
		return nil, fmt.Errorf("project: list environment access: %w", err)
	}
	cells := make([]Cell, 0, len(rows))
	for _, row := range rows {
		role, ok := authz.ParseRole(row.Role)
		if !ok {
			return nil, fmt.Errorf("project: unknown stored role %q", row.Role)
		}
		cells = append(cells, Cell{EnvironmentID: row.EnvironmentID, UserID: row.UserID, Email: row.Email, Name: row.Name, Role: role})
	}
	return cells, nil
}

// SetEnvironmentAccess adds or changes one cell. The user must be a member
// of the environment's project (ErrNotMember otherwise).
func (s *Service) SetEnvironmentAccess(ctx context.Context, environmentID, userID uuid.UUID, role authz.Role) (*Cell, error) {
	if !authz.ValidCellRole(role.String()) {
		return nil, ErrInvalidRole
	}
	env, err := s.GetEnvironment(ctx, environmentID)
	if err != nil {
		return nil, err
	}
	user, err := s.st.GetUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("project: get user: %w", err)
	}
	if _, err := s.st.UpsertEnvironmentAccess(ctx, store.UpsertEnvironmentAccessParams{
		EnvironmentID: env.ID, ProjectID: env.ProjectID, UserID: userID, Role: role.String(),
	}); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" { // foreign_key_violation: no membership
			return nil, ErrNotMember
		}
		return nil, fmt.Errorf("project: set environment access: %w", err)
	}
	return &Cell{EnvironmentID: env.ID, UserID: user.ID, Email: user.Email, Name: user.Name, Role: role}, nil
}

// RemoveEnvironmentAccess drops one cell; the user falls back to their
// project role (capped by the ceiling).
func (s *Service) RemoveEnvironmentAccess(ctx context.Context, environmentID, userID uuid.UUID) error {
	rows, err := s.st.DeleteEnvironmentAccess(ctx, store.DeleteEnvironmentAccessParams{EnvironmentID: environmentID, UserID: userID})
	if err != nil {
		return fmt.Errorf("project: remove environment access: %w", err)
	}
	if rows == 0 {
		return ErrEnvironmentNotFound
	}
	return nil
}

// UpdateEnvironmentSettings replaces the environment's settings. Callers
// merge partial updates onto the stored settings first; promote_from must
// name other environments of the same project.
func (s *Service) UpdateEnvironmentSettings(ctx context.Context, environmentID uuid.UUID, settings authz.Settings) (*store.Environment, error) {
	env, err := s.GetEnvironment(ctx, environmentID)
	if err != nil {
		return nil, err
	}
	if !authz.ValidMaxRole(settings.MaxRole.String()) {
		return nil, fmt.Errorf("%w: max_role", ErrInvalidSettings)
	}
	if !authz.ValidDeployPolicy(settings.DeployPolicy) {
		return nil, fmt.Errorf("%w: deploy_policy must be direct or promote-only", ErrInvalidSettings)
	}
	if !authz.ValidPriority(settings.Priority) {
		return nil, fmt.Errorf("%w: priority must be normal or high", ErrInvalidSettings)
	}
	promoteFrom := settings.PromoteFrom
	if promoteFrom == nil {
		promoteFrom = []string{}
	}
	if len(promoteFrom) > 0 {
		siblings, err := s.st.ListEnvironments(ctx, env.ProjectID)
		if err != nil {
			return nil, fmt.Errorf("project: list environments: %w", err)
		}
		known := make(map[string]bool, len(siblings))
		for _, sibling := range siblings {
			if sibling.ID != env.ID {
				known[sibling.Name] = true
			}
		}
		seen := make(map[string]bool, len(promoteFrom))
		for _, name := range promoteFrom {
			if !known[name] {
				return nil, fmt.Errorf("%w: promote_from names %q, which is not another environment of the project", ErrInvalidSettings, name)
			}
			if seen[name] {
				return nil, fmt.Errorf("%w: promote_from lists %q twice", ErrInvalidSettings, name)
			}
			seen[name] = true
		}
	}
	updated, err := s.st.UpdateEnvironmentSettings(ctx, store.UpdateEnvironmentSettingsParams{
		ID:           env.ID,
		MaxRole:      settings.MaxRole.String(),
		DeployPolicy: settings.DeployPolicy,
		PromoteFrom:  promoteFrom,
		Priority:     settings.Priority,
	})
	if err != nil {
		return nil, fmt.Errorf("project: update environment settings: %w", err)
	}
	return &updated, nil
}

// ResolveUser finds a user by id or email for the access surfaces, which
// take either so project admins can grant by the address they know.
func (s *Service) ResolveUser(ctx context.Context, idOrEmail string) (*store.User, error) {
	var (
		user store.User
		err  error
	)
	if id, parseErr := uuid.Parse(idOrEmail); parseErr == nil {
		user, err = s.st.GetUserByID(ctx, id)
	} else if strings.Contains(idOrEmail, "@") {
		user, err = s.st.GetUserByEmail(ctx, idOrEmail)
	} else {
		return nil, ErrUserNotFound
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("project: resolve user: %w", err)
	}
	return &user, nil
}
