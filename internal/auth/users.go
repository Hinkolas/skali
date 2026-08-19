package auth

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/store"
)

// Instance-wide roles. Roles are code, not data: the DB stores which role a
// user has, never what a role means. Admins may do everything and manage
// users, nodes, and instance settings; members hold nothing until granted
// project membership (internal/authz, docs/permissions.md).
const (
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// ValidRole reports whether s is one of the known instance roles.
func ValidRole(s string) bool {
	return s == RoleAdmin || s == RoleMember
}

// The user-management functions below are package-level (like CreateUser)
// rather than Service methods so the operator CLI can call them with only a
// database connection — no AUTH_SECRET required. Guards that need the caller's
// identity (no self-delete, no self-demote) live in the HTTP layer; the
// last-admin invariant is enforced here so every entry point honours it.

// SetUserRole changes a user's instance role, refusing to demote the last
// admin.
func SetUserRole(ctx context.Context, st *store.Store, userID uuid.UUID, role string) (store.User, error) {
	if !ValidRole(role) {
		return store.User{}, ErrInvalidRole
	}
	var user store.User
	err := st.WithTx(ctx, func(q *store.Queries) error {
		current, err := q.GetUserByID(ctx, userID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if current.Role == role {
			user = current
			return nil
		}
		user, err = q.SetUserRole(ctx, store.SetUserRoleParams{ID: userID, Role: role})
		if err != nil {
			return err
		}
		if current.Role == RoleAdmin {
			return ensureAdminRemains(ctx, q)
		}
		return nil
	})
	if err != nil {
		return store.User{}, err
	}
	return user, nil
}

// SetUserName updates a user's display name.
func SetUserName(ctx context.Context, st *store.Store, userID uuid.UUID, name string) (store.User, error) {
	user, err := st.SetUserName(ctx, store.SetUserNameParams{ID: userID, Name: name})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.User{}, ErrNotFound
		}
		return store.User{}, err
	}
	return user, nil
}

// SetUserCreateProjects grants or revokes a member's right to create
// projects (instance admins always may).
func SetUserCreateProjects(ctx context.Context, st *store.Store, userID uuid.UUID, allowed bool) (store.User, error) {
	user, err := st.SetUserCreateProjects(ctx, store.SetUserCreateProjectsParams{ID: userID, CreateProjects: allowed})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.User{}, ErrNotFound
		}
		return store.User{}, err
	}
	return user, nil
}

// DeleteUser removes a user and (via cascade) their accounts, sessions, and
// 2FA enrollment, refusing to delete the last admin.
func DeleteUser(ctx context.Context, st *store.Store, userID uuid.UUID) error {
	return st.WithTx(ctx, func(q *store.Queries) error {
		user, err := q.GetUserByID(ctx, userID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if _, err := q.DeleteUserByID(ctx, userID); err != nil {
			return err
		}
		if user.Role == RoleAdmin {
			return ensureAdminRemains(ctx, q)
		}
		return nil
	})
}

// ResetUserPassword sets a new password without knowing the old one — the
// admin recovery path (there is no email-based reset by design). Every session
// of the user is revoked.
func ResetUserPassword(ctx context.Context, st *store.Store, userID uuid.UUID, newPassword string) error {
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	phc, err := hashPassword(newPassword)
	if err != nil {
		return err
	}
	return st.WithTx(ctx, func(q *store.Queries) error {
		if _, err := q.GetUserByID(ctx, userID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if err := q.UpdateAccountPassword(ctx, store.UpdateAccountPasswordParams{UserID: userID, Password: &phc}); err != nil {
			return err
		}
		_, err := q.DeleteSessionsByUser(ctx, userID)
		return err
	})
}

// ensureAdminRemains rolls the surrounding transaction back with ErrLastAdmin
// when no admin would be left. The count locks the remaining admin rows, so
// concurrent demotions serialize instead of both slipping past the check.
func ensureAdminRemains(ctx context.Context, q *store.Queries) error {
	n, err := q.CountAdminsForUpdate(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrLastAdmin
	}
	return nil
}
