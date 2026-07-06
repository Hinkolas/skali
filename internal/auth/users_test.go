package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCreateUserRoles(t *testing.T) {
	_, st := newTestService(t)
	ctx := context.Background()

	admin, err := CreateUser(ctx, st, "admin@example.com", "", "hunter2hunter2", RoleAdmin)
	require.NoError(t, err)
	require.Equal(t, RoleAdmin, admin.Role)

	member, err := CreateUser(ctx, st, "dev@example.com", "", "hunter2hunter2", RoleMember)
	require.NoError(t, err)
	require.Equal(t, RoleMember, member.Role)

	_, err = CreateUser(ctx, st, "x@example.com", "", "hunter2hunter2", "owner")
	require.ErrorIs(t, err, ErrInvalidRole)
}

func TestSetUserRoleLastAdminGuard(t *testing.T) {
	_, st := newTestService(t)
	ctx := context.Background()

	admin, err := CreateUser(ctx, st, "admin@example.com", "", "hunter2hunter2", RoleAdmin)
	require.NoError(t, err)
	member, err := CreateUser(ctx, st, "dev@example.com", "", "hunter2hunter2", RoleMember)
	require.NoError(t, err)

	// Demoting the only admin is refused and rolled back.
	_, err = SetUserRole(ctx, st, admin.ID, RoleMember)
	require.ErrorIs(t, err, ErrLastAdmin)
	got, err := st.GetUserByID(ctx, admin.ID)
	require.NoError(t, err)
	require.Equal(t, RoleAdmin, got.Role)

	// With a second admin the demotion goes through.
	_, err = SetUserRole(ctx, st, member.ID, RoleAdmin)
	require.NoError(t, err)
	demoted, err := SetUserRole(ctx, st, admin.ID, RoleMember)
	require.NoError(t, err)
	require.Equal(t, RoleMember, demoted.Role)

	// No-op role change on the (now only) admin is fine.
	kept, err := SetUserRole(ctx, st, member.ID, RoleAdmin)
	require.NoError(t, err)
	require.Equal(t, RoleAdmin, kept.Role)

	_, err = SetUserRole(ctx, st, admin.ID, "owner")
	require.ErrorIs(t, err, ErrInvalidRole)
}

func TestDeleteUserLastAdminGuard(t *testing.T) {
	_, st := newTestService(t)
	ctx := context.Background()

	admin, err := CreateUser(ctx, st, "admin@example.com", "", "hunter2hunter2", RoleAdmin)
	require.NoError(t, err)
	member, err := CreateUser(ctx, st, "dev@example.com", "", "hunter2hunter2", RoleMember)
	require.NoError(t, err)

	// Deleting the only admin is refused and rolled back; members go freely.
	require.ErrorIs(t, DeleteUser(ctx, st, admin.ID), ErrLastAdmin)
	_, err = st.GetUserByID(ctx, admin.ID)
	require.NoError(t, err)

	require.NoError(t, DeleteUser(ctx, st, member.ID))
	require.ErrorIs(t, DeleteUser(ctx, st, member.ID), ErrNotFound)
}

func TestResetUserPassword(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()

	user, err := CreateUser(ctx, st, "dev@example.com", "", "old-password", RoleMember)
	require.NoError(t, err)
	sess, err := svc.Login(ctx, "dev@example.com", "old-password", meta)
	require.NoError(t, err)

	require.ErrorIs(t, ResetUserPassword(ctx, st, user.ID, "short"), ErrWeakPassword)

	require.NoError(t, ResetUserPassword(ctx, st, user.ID, "new-password"))

	// Old sessions are revoked, the old password is dead, the new one works.
	_, _, err = svc.Authenticate(ctx, sess.Session.Token)
	require.ErrorIs(t, err, ErrInvalidToken)
	_, err = svc.Login(ctx, "dev@example.com", "old-password", meta)
	require.ErrorIs(t, err, ErrInvalidCredentials)
	_, err = svc.Login(ctx, "dev@example.com", "new-password", meta)
	require.NoError(t, err)
}
