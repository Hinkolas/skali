package deploy

import (
	"context"
	"errors"
	"github.com/Hinkolas/skali/internal/artifactstore"
	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"strings"
	"sync"
	"testing"
	"time"
)

const routedManifest = `version: "1"
name: demo
applications:
  web:
    image: nginx:alpine
    routes:
      public: {domain: example.com, port: 80}
`

func routePrepared(t *testing.T, f *fixture, env, def uuid.UUID) *Prepared {
	t.Helper()
	p, err := f.deploy.Prepare(context.Background(), PrepareInput{EnvironmentID: env, DefinitionVersionID: def, Resolver: &artifactstore.Fake{Store: f.artifacts, ProjectID: f.projectID}})
	require.NoError(t, err)
	return p
}

func TestHostnameAdmissionAndRelease(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	env2, err := f.projects.CreateEnvironment(ctx, f.projectID, "other", project.EnvironmentOptions{})
	require.NoError(t, err)
	def := f.submit(t, routedManifest, 0)
	first, second := routePrepared(t, f, f.environmentID, def), routePrepared(t, f, env2.ID, def)
	require.NoError(t, f.deploy.Promote(ctx, first))
	err = f.deploy.Promote(ctx, second)
	var conflict *HostnameConflict
	require.ErrorAs(t, err, &conflict)
	require.False(t, conflict.Reserved)
	target, err := f.st.GetEnvironmentTarget(ctx, env2.ID)
	require.NoError(t, err)
	require.Nil(t, target.TargetRevisionID)
	require.Error(t, f.deploy.ReserveHostnames(ctx, []string{"EXAMPLE.COM."}))
	def2 := f.submit(t, strings.Replace(routedManifest, "example.com", "new.example.com", 1), 1)
	require.NoError(t, f.deploy.Promote(ctx, routePrepared(t, f, f.environmentID, def2)))
	require.NoError(t, f.deploy.ReleaseAbsentHostnames(ctx, f.environmentID, map[string]bool{"example.com": true}))
	require.Error(t, f.deploy.Promote(ctx, second))
	require.NoError(t, f.deploy.ReleaseAbsentHostnames(ctx, f.environmentID, map[string]bool{}))
	require.NoError(t, f.deploy.Promote(ctx, second))
	jr := journal.NewService(f.st, "test-rollback")
	_, err = f.deploy.Rollback(ctx, RollbackInput{EnvironmentID: f.environmentID, RevisionID: first.RevisionID, Journal: jr, Actor: "tester"})
	require.ErrorAs(t, err, &conflict)
	target, err = f.st.GetEnvironmentTarget(ctx, f.environmentID)
	require.NoError(t, err)
	require.NotEqual(t, first.RevisionID, *target.TargetRevisionID)
}

func TestConcurrentHostnameClaimsHaveOneWinner(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	def := f.submit(t, routedManifest, 0)
	env2, err := f.projects.CreateEnvironment(ctx, f.projectID, "other", project.EnvironmentOptions{})
	require.NoError(t, err)
	candidates := []*Prepared{routePrepared(t, f, f.environmentID, def), routePrepared(t, f, env2.ID, def)}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, candidate := range candidates {
		wg.Add(1)
		go func() { defer wg.Done(); results <- f.deploy.Promote(ctx, candidate) }()
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		} else {
			var conflict *HostnameConflict
			require.True(t, errors.As(err, &conflict), "%v", err)
		}
	}
	require.Equal(t, 1, wins)
}

func TestReservedHostnameAndPolicyWarnings(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	require.NoError(t, f.deploy.ReserveHostnames(ctx, []string{"example.com"}))
	def := f.submit(t, routedManifest, 0)
	p := routePrepared(t, f, f.environmentID, def)
	err := f.deploy.Promote(ctx, p)
	var conflict *HostnameConflict
	require.ErrorAs(t, err, &conflict)
	require.True(t, conflict.Reserved)
	require.Empty(t, compiler.Warnings(p.Revision.Definition))
	p.Revision.Definition.Backups = map[string]compiler.Backup{"daily": {Schedule: "0 3 * * *", RetentionSeconds: 86400}}
	warnings := compiler.Warnings(p.Revision.Definition)
	require.Len(t, warnings, 1)
	require.Equal(t, "backup_policy_inactive", warnings[0].Code)
	require.Equal(t, []string{"backups.daily"}, warnings[0].Paths)
}

func TestRetiredClaimSurvivesRestartAndTargetLock(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	def := f.submit(t, routedManifest, 0)
	first := routePrepared(t, f, f.environmentID, def)
	require.NoError(t, f.deploy.Promote(ctx, first))
	nextDef := f.submit(t, strings.Replace(routedManifest, "example.com", "next.example.com", 1), 1)
	next := routePrepared(t, f, f.environmentID, nextDef)
	unlock, err := f.st.LockEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	// Another controller/process cannot publish a new target while a stale
	// reconcile pass still owns the lock. Cancellation bounds this assertion.
	blocked, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	require.Error(t, f.deploy.Promote(blocked, next))
	cancel()
	unlock()
	require.NoError(t, f.deploy.Promote(ctx, next))
	restarted := New(f.st, f.values, f.artifacts, "test")
	require.NoError(t, restarted.ReleaseAbsentHostnames(ctx, f.environmentID, map[string]bool{"example.com": true}))
	_, err = f.st.GetHostnameClaim(ctx, "example.com")
	require.NoError(t, err)
	require.NoError(t, restarted.ReleaseAbsentHostnames(ctx, f.environmentID, map[string]bool{}))
	_, err = f.st.GetHostnameClaim(ctx, "example.com")
	require.Error(t, err)
}
