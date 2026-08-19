package project

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/auth"
	"github.com/Hinkolas/skali/internal/authz"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/testdb"
)

func newAccessFixture(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st := store.NewStore(testdb.New(t))
	return New(st), st
}

func createUser(t *testing.T, st *store.Store, email string) *store.User {
	t.Helper()
	u, err := auth.CreateUser(context.Background(), st, email, "", "hunter2hunter2", auth.RoleMember)
	require.NoError(t, err)
	return u
}

func TestCreatorBecomesAdminAndEnvironmentCreatorGetsCell(t *testing.T) {
	t.Parallel()
	svc, st := newAccessFixture(t)
	ctx := context.Background()
	owner := createUser(t, st, "owner@example.com")
	maintainer := createUser(t, st, "maint@example.com")

	proj, err := svc.Create(ctx, "demo", "", owner.ID)
	require.NoError(t, err)
	members, err := svc.ListMembers(ctx, proj.ID)
	require.NoError(t, err)
	require.Len(t, members, 1)
	require.Equal(t, authz.Admin, members[0].Role)
	require.Equal(t, "owner@example.com", members[0].Email)

	// The project admin creating an environment gets no cell; a maintain
	// member does.
	byOwner, err := svc.CreateEnvironment(ctx, proj.ID, "staging", EnvironmentOptions{Creator: owner.ID})
	require.NoError(t, err)
	cells, err := svc.ListEnvironmentAccess(ctx, byOwner.ID)
	require.NoError(t, err)
	require.Empty(t, cells)

	_, err = svc.SetMember(ctx, proj.ID, maintainer.ID, authz.Maintain)
	require.NoError(t, err)
	byMaintainer, err := svc.CreateEnvironment(ctx, proj.ID, "feat-x", EnvironmentOptions{Creator: maintainer.ID})
	require.NoError(t, err)
	cells, err = svc.ListEnvironmentAccess(ctx, byMaintainer.ID)
	require.NoError(t, err)
	require.Len(t, cells, 1)
	require.Equal(t, maintainer.ID, cells[0].UserID)
	require.Equal(t, authz.Admin, cells[0].Role)

	// A non-member creator (instance admin path) gets nothing and no error.
	stranger := createUser(t, st, "stranger@example.com")
	byStranger, err := svc.CreateEnvironment(ctx, proj.ID, "scratch", EnvironmentOptions{Creator: stranger.ID})
	require.NoError(t, err)
	cells, err = svc.ListEnvironmentAccess(ctx, byStranger.ID)
	require.NoError(t, err)
	require.Empty(t, cells)

	// Creation defaults: high priority starts with a read ceiling.
	high, err := svc.CreateEnvironment(ctx, proj.ID, "production", EnvironmentOptions{Priority: authz.PriorityHigh})
	require.NoError(t, err)
	require.Equal(t, "read", high.MaxRole)
	require.Equal(t, "high", high.Priority)
	require.Equal(t, "admin", byOwner.MaxRole)
	require.Equal(t, "normal", byOwner.Priority)
	require.Equal(t, "direct", byOwner.DeployPolicy)
	require.Empty(t, byOwner.PromoteFrom)
}

func TestMembersAndCells(t *testing.T) {
	t.Parallel()
	svc, st := newAccessFixture(t)
	ctx := context.Background()
	bob := createUser(t, st, "bob@example.com")
	dave := createUser(t, st, "dave@example.com")

	proj, err := svc.Create(ctx, "demo", "", uuid.Nil)
	require.NoError(t, err)
	env, err := svc.CreateEnvironment(ctx, proj.ID, "production", EnvironmentOptions{})
	require.NoError(t, err)

	_, err = svc.SetMember(ctx, proj.ID, bob.ID, authz.None)
	require.ErrorIs(t, err, ErrInvalidRole)
	_, err = svc.SetMember(ctx, proj.ID, uuid.New(), authz.Read)
	require.ErrorIs(t, err, ErrUserNotFound)
	_, err = svc.SetMember(ctx, uuid.New(), bob.ID, authz.Read)
	require.ErrorIs(t, err, ErrProjectNotFound)

	// Cells need a membership.
	_, err = svc.SetEnvironmentAccess(ctx, env.ID, dave.ID, authz.Read)
	require.ErrorIs(t, err, ErrNotMember)

	member, err := svc.SetMember(ctx, proj.ID, bob.ID, authz.Read)
	require.NoError(t, err)
	require.Equal(t, authz.Read, member.Role)
	member, err = svc.SetMember(ctx, proj.ID, bob.ID, authz.Maintain)
	require.NoError(t, err)
	require.Equal(t, authz.Maintain, member.Role)

	cell, err := svc.SetEnvironmentAccess(ctx, env.ID, bob.ID, authz.None)
	require.NoError(t, err)
	require.Equal(t, authz.None, cell.Role)
	cells, err := svc.ListEnvironmentAccess(ctx, env.ID)
	require.NoError(t, err)
	require.Len(t, cells, 1)

	// Removing the member drops the cell.
	require.NoError(t, svc.RemoveMember(ctx, proj.ID, bob.ID))
	require.ErrorIs(t, svc.RemoveMember(ctx, proj.ID, bob.ID), ErrNotMember)
	cells, err = svc.ListEnvironmentAccess(ctx, env.ID)
	require.NoError(t, err)
	require.Empty(t, cells)
	require.ErrorIs(t, svc.RemoveEnvironmentAccess(ctx, env.ID, bob.ID), ErrEnvironmentNotFound)

	// ResolveUser by id and by email.
	u, err := svc.ResolveUser(ctx, bob.ID.String())
	require.NoError(t, err)
	require.Equal(t, bob.ID, u.ID)
	u, err = svc.ResolveUser(ctx, "BOB@example.com")
	require.NoError(t, err)
	require.Equal(t, bob.ID, u.ID)
	_, err = svc.ResolveUser(ctx, "nobody@example.com")
	require.ErrorIs(t, err, ErrUserNotFound)
	_, err = svc.ResolveUser(ctx, "not-an-id")
	require.ErrorIs(t, err, ErrUserNotFound)
}

func TestUpdateEnvironmentSettings(t *testing.T) {
	t.Parallel()
	svc, _ := newAccessFixture(t)
	ctx := context.Background()
	proj, err := svc.Create(ctx, "demo", "", uuid.Nil)
	require.NoError(t, err)
	staging, err := svc.CreateEnvironment(ctx, proj.ID, "staging", EnvironmentOptions{})
	require.NoError(t, err)
	production, err := svc.CreateEnvironment(ctx, proj.ID, "production", EnvironmentOptions{})
	require.NoError(t, err)

	updated, err := svc.UpdateEnvironmentSettings(ctx, production.ID, authz.Settings{
		MaxRole: authz.Read, DeployPolicy: authz.DeployPolicyPromoteOnly,
		PromoteFrom: []string{"staging"}, Priority: authz.PriorityHigh,
	})
	require.NoError(t, err)
	require.Equal(t, "read", updated.MaxRole)
	require.Equal(t, "promote-only", updated.DeployPolicy)
	require.Equal(t, []string{"staging"}, updated.PromoteFrom)
	require.Equal(t, "high", updated.Priority)

	_, err = svc.UpdateEnvironmentSettings(ctx, production.ID, authz.Settings{
		MaxRole: authz.Admin, DeployPolicy: "direct", PromoteFrom: []string{"production"}, Priority: "normal",
	})
	require.ErrorIs(t, err, ErrInvalidSettings)
	_, err = svc.UpdateEnvironmentSettings(ctx, production.ID, authz.Settings{
		MaxRole: authz.Admin, DeployPolicy: "direct", PromoteFrom: []string{"nope"}, Priority: "normal",
	})
	require.ErrorIs(t, err, ErrInvalidSettings)
	_, err = svc.UpdateEnvironmentSettings(ctx, production.ID, authz.Settings{
		MaxRole: authz.Admin, DeployPolicy: "yolo", Priority: "normal",
	})
	require.ErrorIs(t, err, ErrInvalidSettings)
	_, err = svc.UpdateEnvironmentSettings(ctx, staging.ID, authz.Settings{
		MaxRole: authz.Admin, DeployPolicy: "direct", Priority: "urgent",
	})
	require.ErrorIs(t, err, ErrInvalidSettings)
	_, err = svc.UpdateEnvironmentSettings(ctx, uuid.New(), authz.Settings{
		MaxRole: authz.Admin, DeployPolicy: "direct", Priority: "normal",
	})
	require.ErrorIs(t, err, ErrEnvironmentNotFound)
}
