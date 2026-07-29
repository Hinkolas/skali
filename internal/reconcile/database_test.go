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
values:
  SESSION_SECRET:
    secret: true
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

func (f *fakeClaims) Suspend(_ context.Context, _ uuid.UUID) error { return nil }

// A database-bearing revision: the application waits visibly on the claim,
// provisioning unblocks it, and activation requires both healthy.
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
	require.NotContains(t, ops, "apply Deployment/"+f.namespace+"/demo-web",
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
	require.Contains(t, f.cluster.recorded(), "apply Deployment/"+f.namespace+"/demo-web")
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
	require.NotContains(t, f.cluster.recorded(), "apply Deployment/"+f.namespace+"/demo-web")

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
