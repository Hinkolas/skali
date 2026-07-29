package database

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/manifest"
	"github.com/Hinkolas/skali/internal/module"
)

func decodeService(t *testing.T) module.Service {
	t.Helper()
	document, err := manifest.Parse([]byte(`version: "1"
name: demo
applications:
  web:
    image: ghcr.io/example/web:1.0.0
    environment:
      DATABASE_URL: "{{ databases.data.url }}"
databases:
  data:
    engine: postgres
    version: 17
`), "skali.yml")
	require.NoError(t, err)
	result, err := compiler.Compile(document)
	require.NoError(t, err)
	service, err := Module{}.Decode(result.Definition, "data")
	require.NoError(t, err)
	return service
}

func TestDecodeAndContract(t *testing.T) {
	t.Parallel()
	service := decodeService(t)
	require.Equal(t, "data", service.Key())
	require.Equal(t, "database", service.Type())
	require.Empty(t, service.Dependencies())
	require.Empty(t, service.Artifacts())
	require.Equal(t, []string{"host", "port", "name", "username", "password", "url"}, service.Outputs())
	require.Equal(t, "claim:databases.data", service.Steps()[0].Key)
	require.True(t, service.Removal().DataLoss)

	_, err := Module{}.Decode(compiler.ProjectDefinition{}, "missing")
	require.Error(t, err)
}

func fresh() module.ObservedResource {
	return module.ObservedResource{Kind: module.KindSource, Name: "kubernetes",
		Source: &module.SourceStatus{State: module.SourceFresh}}
}

func provisionedClaim() module.ObservedResource {
	return module.ObservedResource{Kind: module.KindDatabaseClaim,
		Claim: &module.ClaimStatus{Phase: "provisioned"}}
}

func tenant(status module.DatabaseTenantStatus) module.ObservedResource {
	return module.ObservedResource{Kind: module.KindDatabaseTenant,
		Name: "db_data", DatabaseTenant: &status}
}

func pool(status module.DatabaseClusterStatus) module.ObservedResource {
	return module.ObservedResource{Kind: module.KindDatabaseCluster,
		Name: "pg17-shared", DatabaseCluster: &status}
}

func TestEvaluate(t *testing.T) {
	t.Parallel()
	service := decodeService(t)

	stale := service.Evaluate([]module.ObservedResource{{
		Kind: module.KindSource, Name: "kubernetes",
		Source: &module.SourceStatus{State: module.SourceStale},
	}})
	require.Equal(t, module.HealthUnknown, stale.Health)

	missing := service.Evaluate([]module.ObservedResource{fresh()})
	require.Equal(t, module.HealthUnknown, missing.Health)
	require.Equal(t, "missing-resource", missing.Diagnostics[0].Code)

	pending := service.Evaluate([]module.ObservedResource{fresh(), {
		Kind:  module.KindDatabaseClaim,
		Claim: &module.ClaimStatus{Phase: "pending", Waiting: "no database-capable nodes observed yet"},
	}})
	require.Equal(t, module.HealthProgressing, pending.Health)
	require.Contains(t, pending.Diagnostics[0].Message, "no database-capable nodes")

	// Provisioned alone is not healthy: the tenant and pool observations
	// must agree.
	claimOnly := service.Evaluate([]module.ObservedResource{fresh(), provisionedClaim()})
	require.Equal(t, module.HealthProgressing, claimOnly.Health)
	require.Equal(t, "tenant-unobserved", claimOnly.Diagnostics[0].Code)

	notApplied := service.Evaluate([]module.ObservedResource{fresh(), provisionedClaim(),
		tenant(module.DatabaseTenantStatus{Applied: false, Message: "creating database", Pool: "pg17-shared"})})
	require.Equal(t, module.HealthProgressing, notApplied.Health)
	require.Equal(t, "tenant-not-applied", notApplied.Diagnostics[0].Code)

	healthy := service.Evaluate([]module.ObservedResource{fresh(), provisionedClaim(),
		tenant(module.DatabaseTenantStatus{Applied: true, Pool: "pg17-shared"}),
		pool(module.DatabaseClusterStatus{Instances: 1, ReadyInstances: 1, Primary: "pg17-shared-1"})})
	require.Equal(t, module.HealthHealthy, healthy.Health)
	require.Empty(t, healthy.Diagnostics)

	degraded := service.Evaluate([]module.ObservedResource{fresh(), provisionedClaim(),
		tenant(module.DatabaseTenantStatus{Applied: true, Pool: "pg17-shared"}),
		pool(module.DatabaseClusterStatus{Instances: 2, ReadyInstances: 1, Primary: "pg17-shared-1"})})
	require.Equal(t, module.HealthDegraded, degraded.Health)
	require.Contains(t, degraded.Diagnostics[0].Message, "1/2 instances ready")

	down := service.Evaluate([]module.ObservedResource{fresh(), provisionedClaim(),
		tenant(module.DatabaseTenantStatus{Applied: true, Pool: "pg17-shared"}),
		pool(module.DatabaseClusterStatus{Instances: 1, ReadyInstances: 0})})
	require.Equal(t, module.HealthUnhealthy, down.Health)

	hibernated := service.Evaluate([]module.ObservedResource{fresh(), provisionedClaim(),
		tenant(module.DatabaseTenantStatus{Applied: true, Pool: "pg17-shared"}),
		pool(module.DatabaseClusterStatus{Instances: 1, ReadyInstances: 0, Hibernated: true})})
	require.Equal(t, module.HealthProgressing, hibernated.Health)
	require.Equal(t, "pool-hibernated", hibernated.Diagnostics[0].Code)

	releasing := service.Evaluate([]module.ObservedResource{fresh(), {
		Kind:  module.KindDatabaseClaim,
		Claim: &module.ClaimStatus{Phase: "releasing"},
	}})
	require.Equal(t, module.HealthProgressing, releasing.Health)
	require.Equal(t, "claim-releasing", releasing.Diagnostics[0].Code)
}
