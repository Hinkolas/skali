package deploy

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/artifactstore"
	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/plan"
	"github.com/Hinkolas/skali/internal/revision"
	"github.com/Hinkolas/skali/internal/store"
)

// runDeployment reads back what a run records it changes.
func (f *fixture) runDeployment(t *testing.T, runID uuid.UUID) RunDeployment {
	t.Helper()
	run, err := f.st.GetRunByID(context.Background(), runID)
	require.NoError(t, err)
	require.NotEmpty(t, run.Deployment, "the run records what it changes")
	var document RunDeployment
	require.NoError(t, json.Unmarshal(run.Deployment, &document))
	return document
}

func (f *fixture) activate(t *testing.T, revisionID uuid.UUID) {
	t.Helper()
	_, err := f.st.SetEnvironmentActiveRevision(context.Background(), store.SetEnvironmentActiveRevisionParams{
		EnvironmentID: f.environmentID, ActiveRevisionID: &revisionID,
	})
	require.NoError(t, err)
}

// A deployment's run records the plan its deployer confirmed when it
// opens, and the stored revision's plan from promotion on, where the
// artifact's real digest replaces the pending one.
func TestRunDeploymentFollowsTheDeployment(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	jsvc := journal.NewService(f.st, "executor-1")
	complete := func(deployment *store.Deployment) *ExecuteResult {
		c, err := f.deploy.beginComplete(ctx, deployment.ID, jsvc)
		require.NoError(t, err)
		defer f.deploy.release(deployment.ID)
		result, err := f.deploy.finishComplete(ctx, c)
		require.NoError(t, err)
		return result
	}

	first := f.openForComplete(t, jsvc, f.environmentID, f.submit(t, testManifest, 0), "first-plant-value")
	opened := f.runDeployment(t, *first.RunID)
	require.Empty(t, opened.Revision, "no revision is stored before completion")
	require.Equal(t, []plan.Change{{Service: "applications.web", Action: plan.ActionCreate}}, opened.Changes)
	// Each value names the services that use it.
	web := []string{"applications.web"}
	require.Equal(t, []RunValueChange{
		{ValueChange: plan.ValueChange{Name: "APP_DOMAIN", Action: plan.ActionCreate}, Services: web},
		{ValueChange: plan.ValueChange{Name: "SESSION_SECRET", Action: plan.ActionCreate}, Services: web},
	}, opened.Values)
	require.Empty(t, opened.FromEnvironment)
	require.Empty(t, opened.Restarts)

	result := complete(first)
	promoted := f.runDeployment(t, result.RunID)
	stored, err := f.deploy.GetRevision(ctx, result.RevisionID)
	require.NoError(t, err)
	require.Equal(t, stored.Checksum, promoted.Revision)
	require.Equal(t, opened.Changes, promoted.Changes)
	require.Equal(t, opened.Values, promoted.Values)
	f.activate(t, result.RevisionID)

	// A new image: at open its digest is unknown.
	second := f.openForComplete(t, jsvc, f.environmentID, f.submit(t, changedManifest, 1), "first-plant-value")
	opened = f.runDeployment(t, *second.RunID)
	require.Len(t, opened.Changes, 1)
	require.Equal(t, plan.ActionUpdate, opened.Changes[0].Action)
	require.Contains(t, opened.Changes[0].Detail, "a new artifact replaces")

	result = complete(second)
	promoted = f.runDeployment(t, result.RunID)
	require.Len(t, promoted.Changes, 1)
	require.Equal(t, plan.ActionUpdate, promoted.Changes[0].Action)
	require.Contains(t, promoted.Changes[0].Detail, "artifact "+testDigest[len("sha256:"):][:12]+" replaces")
	require.NotContains(t, promoted.Changes[0].Detail, "a new artifact")
}

// A rollback records the revision it returns to and what it reverses of
// the target it leaves; restarts record the applications they recreate;
// a forced deployment restarts every application.
func TestRunDeploymentRollbackAndRestarts(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	jsvc := journal.NewService(f.st, "executor-1")
	resolver := &artifactstore.Fake{Store: f.artifacts, ProjectID: f.projectID}

	candidate := f.stage(t,
		map[string]string{"APP_DOMAIN": "demo.example.com", "SESSION_SECRET": "rollback-plant-value"})
	first, err := f.execute(t, jsvc, f.submit(t, testManifest, 0), candidate.ID, resolver)
	require.NoError(t, err)
	f.activate(t, first.RevisionID)
	second, err := f.execute(t, jsvc, f.submit(t, changedManifest, 1), uuid.Nil, resolver)
	require.NoError(t, err)
	f.activate(t, second.RevisionID)
	changed := f.runDeployment(t, second.RunID)
	require.Len(t, changed.Changes, 1)
	require.Equal(t, "applications.web", changed.Changes[0].Service)
	require.Equal(t, plan.ActionUpdate, changed.Changes[0].Action)
	require.Empty(t, changed.Values, "the values stayed")

	rolledBack, err := f.deploy.Rollback(ctx, RollbackInput{
		EnvironmentID: f.environmentID, RevisionID: first.RevisionID, Actor: "tester", Journal: jsvc,
	})
	require.NoError(t, err)
	firstRevision, err := f.deploy.GetRevision(ctx, first.RevisionID)
	require.NoError(t, err)
	rollback := f.runDeployment(t, rolledBack.RunID)
	require.Equal(t, firstRevision.Checksum, rollback.Revision)
	require.Equal(t, changed.Changes[0].Service, rollback.Changes[0].Service)
	require.Equal(t, plan.ActionUpdate, rollback.Changes[0].Action)
	require.Empty(t, rollback.Restarts)

	// The rollout to the first revision never activated: rolling forward
	// again reverses that target, though the second still runs.
	again, err := f.deploy.Rollback(ctx, RollbackInput{
		EnvironmentID: f.environmentID, RevisionID: second.RevisionID, Actor: "tester", Journal: jsvc,
	})
	require.NoError(t, err)
	reversed := f.runDeployment(t, again.RunID)
	require.Len(t, reversed.Changes, 1)
	require.Equal(t, "applications.web", reversed.Changes[0].Service)

	one, err := f.deploy.Restart(ctx, RestartInput{
		EnvironmentID: f.environmentID, ApplicationKey: "web", Actor: "tester", Journal: jsvc,
	})
	require.NoError(t, err)
	require.Equal(t, RunDeployment{Restarts: []string{"applications.web"}}, f.runDeployment(t, one.RunID))
	all, err := f.deploy.Restart(ctx, RestartInput{
		EnvironmentID: f.environmentID, Actor: "tester", Journal: jsvc,
	})
	require.NoError(t, err)
	require.Equal(t, RunDeployment{Restarts: []string{"applications.web"}}, f.runDeployment(t, all.RunID))

	forced, err := f.deploy.Execute(ctx, ExecuteInput{
		ProjectID: f.projectID, EnvironmentID: f.environmentID,
		DefinitionVersionID: f.submit(t, testManifest, 2), Resolver: resolver,
		Journal: jsvc, Actor: "tester", Restart: true,
	})
	require.NoError(t, err)
	restarted := f.runDeployment(t, forced.RunID)
	require.Equal(t, []string{"applications.web"}, restarted.Restarts)
}

// Promote reads the revision it replaces in the transaction that moves the
// target: a late activation of the previous target, after the next
// revision was prepared, is the baseline of what it changes.
func TestPromoteRecordsTheRevisionItReplaced(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	resolver := &artifactstore.Fake{Store: f.artifacts, ProjectID: f.projectID}
	candidate := f.stage(t,
		map[string]string{"APP_DOMAIN": "demo.example.com", "SESSION_SECRET": "replaced-plant-value"})

	first, err := f.deploy.Prepare(ctx, PrepareInput{
		EnvironmentID: f.environmentID, DefinitionVersionID: f.submit(t, testManifest, 0),
		CandidateID: candidate.ID, Resolver: resolver,
	})
	require.NoError(t, err)
	require.NoError(t, f.deploy.Promote(ctx, first))
	require.Nil(t, first.Replaced, "nothing was active before the first promotion")

	changedVersion, _, err := f.projects.SubmitCandidate(ctx, f.projectID, []byte(changedManifest), "yaml")
	require.NoError(t, err)
	second, err := f.deploy.Prepare(ctx, PrepareInput{
		EnvironmentID: f.environmentID, DefinitionVersionID: changedVersion, Resolver: resolver,
	})
	require.NoError(t, err)
	f.activate(t, first.RevisionID)
	require.NoError(t, f.deploy.Promote(ctx, second))
	require.NotNil(t, second.Replaced)
	require.Equal(t, first.RevisionID, *second.Replaced)
}

const twoApplicationManifest = `name: demo
applications:
  web:
    image: ghcr.io/example/web:1.0.0
    ports:
      http:
        port: 8080
        protocol: http
    environment:
      APP_DOMAIN: "${APP_DOMAIN}"
      SESSION_SECRET: "${SESSION_SECRET}"
  worker:
    image: ghcr.io/example/worker:1.0.0
`

// An environment-wide restart names the applications of the target it
// stamps: a failed rollout's fallback, which moves the target back under
// the environment lock without a run of its own, cannot land between the
// restart's read and its stamp.
func TestRestartNamesTheTargetItStamps(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	jsvc := journal.NewService(f.st, "executor-1")
	resolver := &artifactstore.Fake{Store: f.artifacts, ProjectID: f.projectID}
	candidate := f.stage(t,
		map[string]string{"APP_DOMAIN": "demo.example.com", "SESSION_SECRET": "fallback-plant-value"})
	first, err := f.execute(t, jsvc, f.submit(t, testManifest, 0), candidate.ID, resolver)
	require.NoError(t, err)
	f.activate(t, first.RevisionID)
	second, err := f.execute(t, jsvc, f.submit(t, twoApplicationManifest, 1), uuid.Nil, resolver)
	require.NoError(t, err)

	// The kernel's pass holds the lock while the second rollout fails and
	// falls back to the first revision.
	unlock, err := f.st.LockEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	restarted := make(chan uuid.UUID, 1)
	go func() {
		result, err := f.deploy.Restart(ctx, RestartInput{
			EnvironmentID: f.environmentID, Actor: "tester", Journal: jsvc,
		})
		if err != nil {
			t.Error(err)
			restarted <- uuid.Nil
			return
		}
		restarted <- result.RunID
	}()
	time.Sleep(200 * time.Millisecond)
	rows, err := f.st.FallbackEnvironmentTarget(ctx, store.FallbackEnvironmentTargetParams{
		EnvironmentID: f.environmentID, TargetRevisionID: &second.RevisionID,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)
	unlock()

	runID := <-restarted
	require.NotEqual(t, uuid.Nil, runID)
	require.Equal(t, []string{"applications.web"}, f.runDeployment(t, runID).Restarts)
}

// A value reaches the services whose environment, route domain, or route
// alias references it.
func TestValueUsers(t *testing.T) {
	t.Parallel()
	variable := func(name string) compiler.Expression {
		return compiler.Expression{Parts: []compiler.ExpressionPart{{Kind: "project_variable", Name: name}}}
	}
	literal := compiler.Expression{Parts: []compiler.ExpressionPart{{Kind: "literal", Value: "example.com"}}}
	rev := &revision.Revision{Definition: compiler.ProjectDefinition{
		Applications: map[string]compiler.Application{
			"web": {
				Environment: map[string]compiler.Expression{"TOKEN": variable("TOKEN"), "AGAIN": variable("TOKEN")},
				Routes: map[string]compiler.Route{"main": {
					Domain: literal, Aliases: []compiler.Expression{variable("LEGACY_DOMAIN")},
				}},
			},
			"api": {Environment: map[string]compiler.Expression{"TOKEN": variable("TOKEN")}},
		},
		Buckets: map[string]compiler.BucketClaim{
			"files": {Route: &compiler.BucketRoute{Domain: variable("FILES_DOMAIN")}},
		},
	}}
	require.Equal(t, map[string][]string{
		"TOKEN":         {"applications.api", "applications.web"},
		"LEGACY_DOMAIN": {"applications.web"},
		"FILES_DOMAIN":  {"buckets.files"},
	}, valueUsers(rev))
}
