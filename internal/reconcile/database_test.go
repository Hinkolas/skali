package reconcile

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/module"
)

const databaseManifest = `version: "1"
name: demo
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
      DATABASE_URL: "{{ databases.data.url }}"
databases:
  data:
    engine: postgres
    version: 17
`

// fakeClaims scripts the claim manager: the kernel must consume readiness
// without knowing claim mechanics.
type fakeClaims struct {
	states []ClaimState
	calls  int
}

func (f *fakeClaims) Ensure(_ context.Context, _ ClaimEnsureInput) ([]ClaimState, error) {
	f.calls++
	return f.states, nil
}

func (f *fakeClaims) Release(_ context.Context, _ uuid.UUID) (bool, []string, error) {
	return true, nil, nil
}

// A database-bearing revision: the application waits visibly on the claim,
// provisioning unblocks it, and activation requires both healthy.
// A provisioned claim whose pool never appeared in the observation (its
// creation fell into an informer-establishment gap) must trigger the
// observation refresh: no watch event will ever heal that gap on its own.
func TestReconcileUnobservedPoolRefreshesObservation(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	claims := &fakeClaims{states: []ClaimState{{Service: "databases.data", Provisioned: true}}}
	f.kernel.deps.Claims = claims
	refreshes := 0
	f.kernel.deps.RefreshObservation = func() { refreshes++ }

	f.executeDeploymentManifest(t, databaseManifest)
	f.fake.SetFresh()
	claimID := uuid.Must(uuid.NewV7())
	f.fake.SetDatabaseClaim(f.environmentID, "databases.data", claimID,
		module.ClaimStatus{Phase: "provisioned"})
	f.fake.SetDatabaseTenant(f.environmentID, "databases.data", "pg17-shared", "db_data",
		module.DatabaseTenantStatus{Applied: true})
	// The pool is deliberately absent: the tenant references pg17-shared
	// but the observation never saw it.

	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, 1, refreshes, "a pool-unobserved pass must ask for an observation refresh")

	// The refresh restores the pool; the following passes proceed to
	// activation without asking again.
	f.fake.SetDatabasePool("pg17-shared",
		module.DatabaseClusterStatus{Instances: 1, ReadyInstances: 1, Primary: "pg17-shared-1"})
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	f.markHealthy(t)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, 1, refreshes, "a healed observation must not refresh again")
	require.NotNil(t, f.target(t).ActiveRevisionID)
}

func TestReconcileDatabaseClaimGatesApplication(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	claims := &fakeClaims{states: []ClaimState{{
		Service: "databases.data", Waiting: "waiting for placement",
	}}}
	f.kernel.deps.Claims = claims
	claimID := uuid.Must(uuid.NewV7())

	result := f.executeDeploymentManifest(t, databaseManifest)
	f.fake.SetFresh()

	// Pass 1: the claim is pending; the application must not apply.
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, 1, claims.calls)
	ops := f.cluster.recorded()
	require.NotContains(t, ops, "apply Deployment/"+f.namespace+"/app-demo-web-714832ea87e5bc991f3f11667354c6c3",
		"the application must wait for the database claim")

	tree, err := f.journal.RunTree(ctx, result.RunID)
	require.NoError(t, err)
	steps := flattenSteps(tree.Steps)
	require.Equal(t, "waiting", steps["claim:databases.data"])
	require.Equal(t, "waiting", steps["apply:web"])

	// The substrate provisions: readiness, the claim projection, and the
	// observed tenant and pool flip together.
	claims.states = []ClaimState{{Service: "databases.data", Provisioned: true}}
	f.fake.SetDatabaseClaim(f.environmentID, "databases.data", claimID,
		module.ClaimStatus{Phase: "provisioned"})
	f.fake.SetDatabaseTenant(f.environmentID, "databases.data", "pg17-shared", "db_data",
		module.DatabaseTenantStatus{Applied: true})
	f.fake.SetDatabasePool("pg17-shared",
		module.DatabaseClusterStatus{Instances: 1, ReadyInstances: 1, Primary: "pg17-shared-1"})

	// Pass 2 applies the application; health arrives; pass 3 activates.
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Contains(t, f.cluster.recorded(), "apply Deployment/"+f.namespace+"/app-demo-web-714832ea87e5bc991f3f11667354c6c3")
	f.markHealthy(t)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)

	target := f.target(t)
	require.NotNil(t, target.ActiveRevisionID)
	require.Equal(t, *target.TargetRevisionID, *target.ActiveRevisionID)

	tree, err = f.journal.RunTree(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", tree.Run.Status)
	steps = flattenSteps(tree.Steps)
	require.Equal(t, "succeeded", steps["claim:databases.data"])
	require.Equal(t, "succeeded", steps["apply:web"])
	require.Equal(t, "succeeded", steps["activate"])

	// The status projection carries both services with their types.
	status, err := f.kernel.Status(ctx, f.environmentID)
	require.NoError(t, err)
	types := map[string]string{}
	health := map[string]module.Health{}
	for _, service := range status.Services {
		types[service.Type+"."+service.Key] = service.Type
		health[service.Type+"."+service.Key] = service.Health
	}
	require.Equal(t, "database", types["database.data"])
	require.Equal(t, module.HealthHealthy, health["database.data"])
	require.Equal(t, module.HealthHealthy, health["application.web"])
}

// TestReconcileClaimProvisionAppliesWithoutProjections pins the incident
// regression (2026-07-31): once the substrate reports a claim provisioned,
// the dependent application applies on the next pass even when every
// observed projection lags behind (informer or poll delivery); activation
// still waits for real observed health.
func TestReconcileClaimProvisionAppliesWithoutProjections(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	claims := &fakeClaims{states: []ClaimState{{
		Service: "databases.data", Waiting: "waiting for placement",
	}}}
	f.kernel.deps.Claims = claims

	f.executeDeploymentManifest(t, databaseManifest)
	f.fake.SetFresh()

	// Pass 1: blocked on the claim, and the pass arms its own requeue so
	// progress never depends on an informer event arriving.
	requeue, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, requeueHealthCheck, requeue)
	require.NotContains(t, f.cluster.recorded(), "apply Deployment/"+f.namespace+"/app-demo-web-714832ea87e5bc991f3f11667354c6c3")

	// The substrate flips ONLY the fresh claim state; the observed claim,
	// tenant, and pool projections all stay stale.
	claims.states = []ClaimState{{Service: "databases.data", Provisioned: true}}

	requeue, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Contains(t, f.cluster.recorded(), "apply Deployment/"+f.namespace+"/app-demo-web-714832ea87e5bc991f3f11667354c6c3",
		"a provisioned claim must not withhold the workload on projection lag")
	require.Equal(t, requeueHealthCheck, requeue, "the pass keeps its cadence until healthy")

	// Activation still requires observed health, which has not arrived.
	target := f.target(t)
	require.Nil(t, target.ActiveRevisionID)
}

// Without a substrate (API-only mode) database services wait visibly
// instead of failing the pass.
func TestReconcileDatabaseWithoutSubstrateWaits(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	result := f.executeDeploymentManifest(t, databaseManifest)
	f.fake.SetFresh()

	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.NotContains(t, f.cluster.recorded(), "apply Deployment/"+f.namespace+"/app-demo-web-714832ea87e5bc991f3f11667354c6c3")

	tree, err := f.journal.RunTree(ctx, result.RunID)
	require.NoError(t, err)
	steps := flattenSteps(tree.Steps)
	require.Equal(t, "waiting", steps["claim:databases.data"])
}

func flattenSteps(steps []*journal.TreeStep) map[string]string {
	flat := map[string]string{}
	var collect func(steps []*journal.TreeStep)
	collect = func(steps []*journal.TreeStep) {
		for _, step := range steps {
			flat[step.Step.Key] = step.Step.Status
			collect(step.Children)
		}
	}
	collect(steps)
	return flat
}
