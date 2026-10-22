package database

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/manifest"
	"github.com/Hinkolas/skali/internal/module"
)

func decodeService(t *testing.T) module.Service {
	t.Helper()
	document, err := manifest.Parse([]byte(`name: demo
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

func poolResource(status module.DatabaseClusterStatus) module.ObservedResource {
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
		poolResource(module.DatabaseClusterStatus{Instances: 1, ReadyInstances: 1, Primary: "pg17-shared-1"})})
	require.Equal(t, module.HealthHealthy, healthy.Health)
	require.Empty(t, healthy.Diagnostics)

	degraded := service.Evaluate([]module.ObservedResource{fresh(), provisionedClaim(),
		tenant(module.DatabaseTenantStatus{Applied: true, Pool: "pg17-shared"}),
		poolResource(module.DatabaseClusterStatus{Instances: 2, ReadyInstances: 1, Primary: "pg17-shared-1"})})
	require.Equal(t, module.HealthDegraded, degraded.Health)
	require.Contains(t, degraded.Diagnostics[0].Message, "1/2 instances ready")

	down := service.Evaluate([]module.ObservedResource{fresh(), provisionedClaim(),
		tenant(module.DatabaseTenantStatus{Applied: true, Pool: "pg17-shared"}),
		poolResource(module.DatabaseClusterStatus{Instances: 1, ReadyInstances: 0})})
	require.Equal(t, module.HealthUnhealthy, down.Health)

	releasing := service.Evaluate([]module.ObservedResource{fresh(), {
		Kind:  module.KindDatabaseClaim,
		Claim: &module.ClaimStatus{Phase: "releasing"},
	}})
	require.Equal(t, module.HealthProgressing, releasing.Health)
	require.Equal(t, "claim-releasing", releasing.Diagnostics[0].Code)
}

func connectionSource(state string) module.ObservedResource {
	status := module.SourceStatus{State: state}
	if state == module.SourceStale {
		status.StaleSince = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	}
	return module.ObservedResource{Kind: module.KindSource, Name: module.SourceDatabaseConnections, Source: &status}
}

func connections(state, reason string) module.ObservedResource {
	return module.ObservedResource{Kind: module.KindDatabaseConnections, Name: "pg17-shared",
		DatabaseConnections: &module.DatabaseConnectionsStatus{State: state, Reason: reason, Used: 95, Limit: 97}}
}

// A ready pool is not a pool applications can connect to: the connection
// check decides. Only a confirmed refusal is a fault that holds a rollout;
// a pool close to its limit and a check that cannot run are advisory, and
// none of them reads as healthy.
func TestEvaluateConnections(t *testing.T) {
	t.Parallel()
	service := decodeService(t)
	evaluate := func(pool module.DatabaseClusterStatus, extra ...module.ObservedResource) module.Evaluation {
		observed := []module.ObservedResource{fresh(), provisionedClaim(),
			tenant(module.DatabaseTenantStatus{Applied: true, Pool: "pg17-shared"})}
		observed = append(observed, extra...)
		return service.Evaluate(append(observed, poolResource(pool)))
	}
	ready := module.DatabaseClusterStatus{Instances: 1, ReadyInstances: 1, Primary: "pg17-shared-1"}

	// Nothing checks connections (API-only, a test): the instances decide.
	require.Equal(t, module.HealthHealthy, evaluate(ready).Health)

	healthy := evaluate(ready, connectionSource(module.SourceFresh), connections(module.ConnectionsAvailable, ""))
	require.Equal(t, module.Evaluation{Health: module.HealthHealthy, Diagnostics: []module.Diagnostic{}}, healthy)

	refusing := evaluate(ready, connectionSource(module.SourceFresh),
		connections(module.ConnectionsRefusing, "remaining connection slots are reserved for roles with the SUPERUSER attribute"))
	require.Equal(t, module.HealthUnhealthy, refusing.Health)
	require.False(t, refusing.Advisory, "applications cannot connect: the database is at fault")
	require.Len(t, refusing.Diagnostics, 1)
	require.Equal(t, "connections-exhausted", refusing.Diagnostics[0].Code)
	require.Equal(t, "error", refusing.Diagnostics[0].Severity)
	require.Contains(t, refusing.Diagnostics[0].Message, "pool pg17-shared refuses new connections "+
		"(remaining connection slots are reserved for roles with the SUPERUSER attribute)")
	require.NotContains(t, refusing.Diagnostics[0].Message, "95", "the pool's usage is an operator's view")

	saturated := evaluate(ready, connectionSource(module.SourceFresh), connections(module.ConnectionsSaturated, "90% or more"))
	require.Equal(t, module.HealthDegraded, saturated.Health)
	require.True(t, saturated.Advisory)
	require.Equal(t, "connections-high", saturated.Diagnostics[0].Code)

	unknown := evaluate(ready, connectionSource(module.SourceFresh), connections(module.ConnectionsUnknown, "no ready primary instance to check"))
	require.Equal(t, module.HealthUnknown, unknown.Health)
	require.True(t, unknown.Advisory)
	require.Contains(t, unknown.Diagnostics[0].Message, "no ready primary instance to check")

	// A stale or never-completed check is unknown, never the last verdict.
	stale := evaluate(ready, connectionSource(module.SourceStale), connections(module.ConnectionsAvailable, ""))
	require.Equal(t, module.HealthUnknown, stale.Health)
	require.True(t, stale.Advisory)
	require.Contains(t, stale.Diagnostics[0].Message, "stale since 2026-10-22T12:00:00Z")
	pending := evaluate(ready, connectionSource(module.SourceUnknown))
	require.Equal(t, module.HealthUnknown, pending.Health)
	missing := evaluate(ready, connectionSource(module.SourceFresh))
	require.Equal(t, module.HealthUnknown, missing.Health)
	require.Contains(t, missing.Diagnostics[0].Message, "has not completed yet")

	// A degraded pool is a fault of its own; joined with an advisory
	// verdict the result is no longer advisory.
	short := module.DatabaseClusterStatus{Instances: 2, ReadyInstances: 1, Primary: "pg17-shared-1"}
	both := evaluate(short, connectionSource(module.SourceFresh), connections(module.ConnectionsSaturated, ""))
	require.Equal(t, module.HealthDegraded, both.Health)
	require.False(t, both.Advisory)
	require.Len(t, both.Diagnostics, 2)
	worse := evaluate(short, connectionSource(module.SourceFresh), connections(module.ConnectionsRefusing, "sorry, too many clients already"))
	require.Equal(t, module.HealthUnhealthy, worse.Health)

	// No ready instance at all needs no connection check to explain it.
	down := evaluate(module.DatabaseClusterStatus{Instances: 1}, connectionSource(module.SourceFresh),
		connections(module.ConnectionsUnknown, "no ready primary instance to check"))
	require.Equal(t, module.HealthUnhealthy, down.Health)
	require.Equal(t, "pool-unavailable", down.Diagnostics[0].Code)
}
