package project

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/authz"
)

func TestRenameEnvironmentPreservesIdentityAndPromotionRules(t *testing.T) {
	t.Parallel()
	s := newTestService(t)
	ctx := context.Background()
	p, err := s.Create(ctx, "rename-test", "", uuid.Nil)
	require.NoError(t, err)
	source, err := s.CreateEnvironment(ctx, p.ID, "staging", EnvironmentOptions{})
	require.NoError(t, err)
	target, err := s.CreateEnvironment(ctx, p.ID, "production", EnvironmentOptions{})
	require.NoError(t, err)
	settings, err := authz.SettingsOf(target)
	require.NoError(t, err)
	settings.DeployPolicy, settings.PromoteFrom = "promote-only", []string{"staging"}
	_, err = s.UpdateEnvironmentSettings(ctx, target.ID, settings)
	require.NoError(t, err)

	renamed, err := s.RenameEnvironment(ctx, source.ID, "kilohertz")
	require.NoError(t, err)
	require.Equal(t, source.ID, renamed.ID)
	require.Equal(t, source.BackupNamespace, renamed.BackupNamespace)
	require.Equal(t, source.CreatedAt, renamed.CreatedAt)
	require.Equal(t, []string{"staging"}, renamed.PreviousNames)
	target, err = s.GetEnvironment(ctx, target.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"kilohertz"}, target.PromoteFrom)
	// A delayed settings save may still name the alias; canonicalize it.
	updated, err := s.UpdateEnvironmentSettings(ctx, target.ID, settings)
	require.NoError(t, err)
	require.Equal(t, []string{"kilohertz"}, updated.PromoteFrom)
	_, err = s.CreateEnvironment(ctx, p.ID, "staging", EnvironmentOptions{})
	require.ErrorIs(t, err, ErrEnvironmentNameTaken)
	_, err = s.RenameEnvironment(ctx, target.ID, "staging")
	require.ErrorIs(t, err, ErrEnvironmentNameTaken)
	_, err = s.RenameEnvironment(ctx, source.ID, "Bad Name")
	require.ErrorIs(t, err, ErrInvalidName)
	_, err = s.RenameEnvironment(ctx, source.ID, "production")
	require.ErrorIs(t, err, ErrEnvironmentNameTaken)
	// Renaming back is allowed and must not accumulate duplicate aliases.
	renamed, err = s.RenameEnvironment(ctx, source.ID, "staging")
	require.NoError(t, err)
	require.Equal(t, []string{"kilohertz"}, renamed.PreviousNames)

	// Independent names competing for one new name serialize on the project.
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []uuid.UUID{source.ID, target.ID} {
		wg.Go(func() { _, err := s.RenameEnvironment(ctx, id, "shared"); results <- err })
	}
	wg.Wait()
	close(results)
	wins, conflicts := 0, 0
	for err := range results {
		if err == nil {
			wins++
		} else {
			require.ErrorIs(t, err, ErrEnvironmentNameTaken)
			conflicts++
		}
	}
	require.Equal(t, 1, wins)
	require.Equal(t, 1, conflicts)
}
