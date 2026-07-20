package project

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/testdb"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	pool := testdb.New(t)
	return New(store.NewStore(pool))
}

func TestProjectCRUD(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	proj, err := svc.Create(ctx, "demo", "Demo Project")
	require.NoError(t, err)
	require.Equal(t, "demo", proj.Name)
	require.Equal(t, "managed", proj.SourceMode)

	_, err = svc.Create(ctx, "demo", "")
	require.ErrorIs(t, err, ErrProjectNameTaken)
	_, err = svc.Create(ctx, "Not-Valid", "")
	require.ErrorIs(t, err, ErrInvalidName)

	got, err := svc.Get(ctx, proj.ID)
	require.NoError(t, err)
	require.Equal(t, proj.ID, got.ID)

	list, err := svc.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)

	displayName := "Renamed"
	sourceMode := "file"
	updated, err := svc.Update(ctx, proj.ID, &displayName, &sourceMode)
	require.NoError(t, err)
	require.Equal(t, "Renamed", updated.DisplayName)
	require.Equal(t, "file", updated.SourceMode)

	bad := "git"
	_, err = svc.Update(ctx, proj.ID, nil, &bad)
	require.ErrorIs(t, err, ErrInvalidSourceMode)

	require.NoError(t, svc.Delete(ctx, proj.ID))
	require.ErrorIs(t, svc.Delete(ctx, proj.ID), ErrProjectNotFound)
	_, err = svc.Get(ctx, proj.ID)
	require.ErrorIs(t, err, ErrProjectNotFound)
}

func TestEnvironmentCRUD(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	proj, err := svc.Create(ctx, "demo", "")
	require.NoError(t, err)

	env, err := svc.CreateEnvironment(ctx, proj.ID, "production")
	require.NoError(t, err)
	require.Equal(t, "production", env.Name)

	_, err = svc.CreateEnvironment(ctx, proj.ID, "production")
	require.ErrorIs(t, err, ErrEnvironmentNameTaken)
	_, err = svc.CreateEnvironment(ctx, proj.ID, "Bad Name")
	require.ErrorIs(t, err, ErrInvalidName)
	_, err = svc.CreateEnvironment(ctx, uuid.New(), "production")
	require.ErrorIs(t, err, ErrProjectNotFound)

	list, err := svc.ListEnvironments(ctx, proj.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)

	got, err := svc.GetEnvironment(ctx, env.ID)
	require.NoError(t, err)
	require.Equal(t, env.ID, got.ID)

	require.NoError(t, svc.DeleteEnvironment(ctx, env.ID))
	require.ErrorIs(t, svc.DeleteEnvironment(ctx, env.ID), ErrEnvironmentNotFound)
}
