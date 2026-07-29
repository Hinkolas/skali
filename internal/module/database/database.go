// Package database is the production database module: the service-module
// contract implementation for manifest databases backed by the shared
// substrate. It renders no Kubernetes objects itself; the substrate
// controller provisions claims, pools, and tenants, and this module's pure
// evaluation projects the claim's observed state into service health.
package database

import (
	"fmt"
	"time"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/module"
)

type Module struct{}

func (Module) Type() string { return "database" }

func (Module) Decode(definition compiler.ProjectDefinition, key string) (module.Service, error) {
	database, ok := definition.Databases[key]
	if !ok {
		return nil, fmt.Errorf("database: database %s is not defined", key)
	}
	return &service{key: key, database: database}, nil
}

type service struct {
	key      string
	database compiler.DatabaseClaim
}

func (s *service) Key() string  { return s.key }
func (s *service) Type() string { return "database" }

// Dependencies: a database depends on nothing; applications depend on it.
func (s *service) Dependencies() []string { return nil }

// Outputs mirror the compiler's expression catalog for databases.
func (s *service) Outputs() []string {
	return []string{"host", "port", "name", "username", "password", "url"}
}

func (s *service) Artifacts() []module.ArtifactRequirement { return nil }

func (s *service) Steps() []module.Step {
	return []module.Step{
		{Key: "claim:" + s.key, Title: "Provision databases." + s.key},
	}
}

func (s *service) Removal() module.Removal {
	return module.Removal{
		DataLoss:    true,
		Description: "deletes the logical database and its data",
	}
}

// Evaluate projects database health from the substrate's claim observation:
// provisioned means consumers may bind (healthy), earlier phases are
// progressing with the claim's waiting reason, and releasing reflects the
// persisted destructive decision. Pool topology joins the evaluation with
// the dynamic CNPG observation source.
func (s *service) Evaluate(observed []module.ObservedResource) module.Evaluation {
	if source := module.StaleSource(observed); source != nil {
		message := "observation source state is " + source.State
		if !source.StaleSince.IsZero() {
			message = "observation is stale since " + source.StaleSince.UTC().Format(time.RFC3339)
		}
		return module.Evaluation{Health: module.HealthUnknown, Diagnostics: []module.Diagnostic{{
			Severity: "warning", Code: "observation_stale_since", Message: message,
		}}}
	}

	var status *module.ClaimStatus
	for _, resource := range observed {
		if resource.Kind == module.KindDatabaseClaim && resource.Claim != nil {
			status = resource.Claim
			break
		}
	}
	if status == nil {
		return module.Evaluation{Health: module.HealthUnknown, Diagnostics: []module.Diagnostic{{
			Severity: "warning", Code: "missing-resource",
			Message: "no claim observed for databases." + s.key,
		}}}
	}

	switch claim.Phase(status.Phase) {
	case claim.PhaseProvisioned:
		return evaluateProvisioned(observed)
	case claim.PhaseReleasing, claim.PhaseReleased:
		return module.Evaluation{Health: module.HealthProgressing, Diagnostics: []module.Diagnostic{{
			Severity: "info", Code: "claim-releasing",
			Message: "the database is being released",
		}}}
	default:
		message := status.Waiting
		if message == "" {
			message = "waiting for placement and provisioning"
		}
		return module.Evaluation{Health: module.HealthProgressing, Diagnostics: []module.Diagnostic{{
			Severity: "info", Code: "claim-" + status.Phase,
			Message: message,
		}}}
	}
}

// evaluateProvisioned composes a provisioned claim with the observed tenant
// and pool: healthy only when the tenant is reconciled and the pool serves.
// Missing projections read as progressing (informer lag), never healthy.
func evaluateProvisioned(observed []module.ObservedResource) module.Evaluation {
	var tenant *module.DatabaseTenantStatus
	for _, resource := range observed {
		if resource.Kind == module.KindDatabaseTenant && resource.DatabaseTenant != nil {
			tenant = resource.DatabaseTenant
			break
		}
	}
	if tenant == nil {
		return progressing("tenant-unobserved", "waiting for the database observation to catch up")
	}
	if !tenant.Applied {
		message := tenant.Message
		if message == "" {
			message = "the database is not reconciled yet"
		}
		return progressing("tenant-not-applied", message)
	}
	var pool *module.DatabaseClusterStatus
	for _, resource := range observed {
		if resource.Kind == module.KindDatabaseCluster && resource.DatabaseCluster != nil &&
			resource.Name == tenant.Pool {
			pool = resource.DatabaseCluster
			break
		}
	}
	switch {
	case pool == nil:
		return progressing("pool-unobserved", "waiting for the pool observation to catch up")
	case pool.Hibernated:
		return progressing("pool-hibernated", "the pool is hibernated and resumes with the next deployment")
	case pool.ReadyInstances == 0:
		return module.Evaluation{Health: module.HealthUnhealthy, Diagnostics: []module.Diagnostic{{
			Severity: "error", Code: "pool-unavailable",
			Message: "no ready database instances on pool " + tenant.Pool,
		}}}
	case pool.ReadyInstances < pool.Instances:
		return module.Evaluation{Health: module.HealthDegraded, Diagnostics: []module.Diagnostic{{
			Severity: "warning", Code: "pool-degraded",
			Message: fmt.Sprintf("%d/%d instances ready on pool %s (primary %s)",
				pool.ReadyInstances, pool.Instances, tenant.Pool, pool.Primary),
		}}}
	}
	return module.Evaluation{Health: module.HealthHealthy}
}

func progressing(code, message string) module.Evaluation {
	return module.Evaluation{Health: module.HealthProgressing, Diagnostics: []module.Diagnostic{{
		Severity: "info", Code: code, Message: message,
	}}}
}
