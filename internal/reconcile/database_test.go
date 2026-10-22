package reconcile

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/module"
)

const databaseManifest = `name: demo
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
	states      []ClaimState
	generations map[string]string
	calls       int
	// repairs and releases record each Ensure's Repair and Release.
	repairs  []bool
	releases []bool
	// dropped lists claims the revisions no longer declare; Ensure reports
	// them Retained until a pass allows their release.
	dropped []string
}

func (f *fakeClaims) Ensure(_ context.Context, in ClaimEnsureInput) ([]ClaimState, error) {
	f.calls++
	f.repairs = append(f.repairs, in.Repair)
	f.releases = append(f.releases, in.Release)
	states := f.states
	if !in.Release {
		for _, service := range f.dropped {
			states = append(states[:len(states):len(states)], ClaimState{Service: service, Retained: true})
		}
	} else {
		f.dropped = nil
	}
	return states, nil
}

func (f *fakeClaims) Release(_ context.Context, _ uuid.UUID) (bool, []ClaimRelease, error) {
	return true, nil, nil
}

func (f *fakeClaims) Outputs(_ context.Context, _ uuid.UUID) (ClaimOutputs, error) {
	return ClaimOutputs{Generations: f.generations}, nil
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

// The first pass of a rollout asks for every claim, settled ones too, so
// a deploy never waits on the repair cadence for a deleted output Secret;
// later passes do not, until the environment's health turns bad.
func TestPassesAskForEveryClaimOnRolloutAndHealthDrop(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	claims := &fakeClaims{states: []ClaimState{{Service: "databases.data", Provisioned: true}}}
	f.kernel.deps.Claims = claims
	f.executeDeploymentManifest(t, databaseManifest)
	f.fake.SetFresh()
	f.fake.SetDatabaseClaim(f.environmentID, "databases.data", uuid.Must(uuid.NewV7()),
		module.ClaimStatus{Phase: "provisioned"})
	f.fake.SetDatabaseTenant(f.environmentID, "databases.data", "pg17-shared", "db_data",
		module.DatabaseTenantStatus{Applied: true})
	f.fake.SetDatabasePool("pg17-shared",
		module.DatabaseClusterStatus{Instances: 1, ReadyInstances: 1, Primary: "pg17-shared-1"})
	pass := func() bool {
		t.Helper()
		_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
		require.NoError(t, err)
		return claims.repairs[len(claims.repairs)-1]
	}

	require.True(t, pass(), "the rollout's first pass asks for every claim")
	require.False(t, pass(), "the rollout's later passes do not")
	f.markHealthy(t)
	require.False(t, pass(), "nor does its activation")
	require.NotNil(t, f.target(t).ActiveRevisionID)

	f.kernel.recordHealth(f.environmentID, []ServiceStatus{{Health: module.HealthUnhealthy}}, time.Now())
	require.True(t, pass(), "a health drop asks again")
	require.False(t, pass(), "once")
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
	require.NotContains(t, ops, "apply Deployment/"+f.namespace+"/"+f.webDeploymentName(t),
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
	require.Contains(t, f.cluster.recorded(), "apply Deployment/"+f.namespace+"/"+f.webDeploymentName(t))
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
	require.NotContains(t, f.cluster.recorded(), "apply Deployment/"+f.namespace+"/"+f.webDeploymentName(t))

	// The substrate flips ONLY the fresh claim state; the observed claim,
	// tenant, and pool projections all stay stale.
	claims.states = []ClaimState{{Service: "databases.data", Provisioned: true}}

	requeue, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Contains(t, f.cluster.recorded(), "apply Deployment/"+f.namespace+"/"+f.webDeploymentName(t),
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
	require.NotContains(t, f.cluster.recorded(), "apply Deployment/"+f.namespace+"/"+f.webDeploymentName(t))

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

// provisionData makes the database claim of databaseManifest provisioned in
// both the fake claim manager and the observation.
func (f *kernelFixture) provisionData(claims *fakeClaims) {
	claims.states = []ClaimState{{Service: "databases.data", Provisioned: true}}
	f.fake.SetDatabaseClaim(f.environmentID, "databases.data", uuid.Must(uuid.NewV7()),
		module.ClaimStatus{Phase: "provisioned"})
	f.fake.SetDatabaseTenant(f.environmentID, "databases.data", "pg17-shared", "db_data",
		module.DatabaseTenantStatus{Applied: true})
	f.fake.SetDatabasePool("pg17-shared",
		module.DatabaseClusterStatus{Instances: 1, ReadyInstances: 1, Primary: "pg17-shared-1"})
}

// A revision that drops a database releases it only once it is active: the
// passes of its rollout keep the claim, and the activation enqueues the
// pass that releases it. A revision declaring no claim at all still goes
// through the claim manager, or its predecessor's claims would never go.
func TestDroppedClaimsReleaseAfterActivation(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	claims := &fakeClaims{}
	f.kernel.deps.Claims = claims
	f.executeDeploymentManifest(t, databaseManifest)
	f.fake.SetFresh()
	f.provisionData(claims)
	pass := func() {
		t.Helper()
		_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
		require.NoError(t, err)
	}
	pass()
	f.markHealthy(t)
	pass()
	require.Equal(t, *f.target(t).TargetRevisionID, *f.target(t).ActiveRevisionID)

	f.executeDeploymentManifest(t, kernelManifest)
	claims.states, claims.dropped = nil, []string{"databases.data"}
	claims.releases = nil
	f.drainQueue(t)
	pass()
	require.Equal(t, []bool{false}, claims.releases, "the rollout keeps the dropped claim")
	require.Zero(t, f.kernel.queue.Len())

	f.markHealthy(t)
	pass()
	require.Equal(t, *f.target(t).TargetRevisionID, *f.target(t).ActiveRevisionID)
	require.Equal(t, []bool{false, false}, claims.releases, "the activating pass still ran a rollout")
	require.Equal(t, 1, f.kernel.queue.Len(), "activation enqueues the release")

	pass()
	require.Equal(t, []bool{false, false, true}, claims.releases)
	f.drainQueue(t)
	pass()
	require.Zero(t, f.kernel.queue.Len(), "nothing retained, nothing enqueued")
}

// A rollout that drops a database and falls back never allows its release:
// the fallback target declares it again.
func TestDroppedClaimsSurviveFallback(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Nanosecond})
	ctx := context.Background()
	claims := &fakeClaims{}
	f.kernel.deps.Claims = claims
	f.executeDeploymentManifest(t, databaseManifest)
	f.fake.SetFresh()
	f.provisionData(claims)
	f.markHealthy(t)
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	active := *f.target(t).ActiveRevisionID

	f.executeDeploymentManifest(t, strings.Replace(kernelManifest, ":1.0.0", ":2.0.0", 1))
	claims.states, claims.dropped = nil, []string{"databases.data"}
	claims.releases = nil
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, active, *f.target(t).TargetRevisionID, "the rollout fell back")
	require.Equal(t, []bool{false}, claims.releases)
	require.Equal(t, []string{"databases.data"}, claims.dropped, "nothing was released")
}

// A pool close to its connection limit shows in the environment's health
// but never holds a rollout: the deployment activates. A pool that refuses
// new connections holds it, since the new revision could not connect.
func TestPoolConnectionsHoldActivationOnlyWhenRefusing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		state     string
		health    module.Health
		activates bool
	}{
		{module.ConnectionsSaturated, module.HealthDegraded, true},
		{module.ConnectionsUnknown, module.HealthUnknown, true},
		{module.ConnectionsRefusing, module.HealthUnhealthy, false},
	} {
		t.Run(tc.state, func(t *testing.T) {
			t.Parallel()
			f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
			ctx := context.Background()
			f.kernel.deps.Claims = &fakeClaims{states: []ClaimState{{Service: "databases.data", Provisioned: true}}}
			f.executeDeploymentManifest(t, databaseManifest)
			f.fake.SetFresh()
			f.fake.SetDatabaseClaim(f.environmentID, "databases.data", uuid.Must(uuid.NewV7()),
				module.ClaimStatus{Phase: "provisioned"})
			f.fake.SetDatabaseTenant(f.environmentID, "databases.data", "pg17-shared", "db_data",
				module.DatabaseTenantStatus{Applied: true})
			f.fake.SetDatabasePool("pg17-shared",
				module.DatabaseClusterStatus{Instances: 1, ReadyInstances: 1, Primary: "pg17-shared-1"})
			f.fake.SetSourceFresh(module.SourceDatabaseConnections)
			f.fake.SetDatabaseConnections("pg17-shared", module.DatabaseConnectionsStatus{
				State: tc.state, Reason: "sorry, too many clients already",
			})

			_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
			require.NoError(t, err)
			f.markHealthy(t)
			_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
			require.NoError(t, err)

			require.Equal(t, tc.activates, f.target(t).ActiveRevisionID != nil)
			health, ok := f.kernel.EnvironmentHealth(f.environmentID)
			require.True(t, ok)
			require.Equal(t, tc.health, health.Health, "the rollup shows the pool's state either way")
		})
	}
}
