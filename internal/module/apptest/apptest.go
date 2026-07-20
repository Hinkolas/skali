// Package apptest is the application TEST module: a minimal but honest
// implementation of the module contract used to exercise the registry, the
// dependency graph, and the pure health evaluation in R1 tests. The real
// application module (Deployment, Service, routing, scaling, logs) replaces
// it in R3 without changing the interface; that swap is the acceptance test
// for the section 6.7 envelope.
package apptest

import (
	"fmt"
	"sort"
	"time"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/module"
)

type Module struct{}

func (Module) Type() string { return "application" }

func (Module) Decode(definition compiler.ProjectDefinition, key string) (module.Service, error) {
	application, ok := definition.Applications[key]
	if !ok {
		return nil, fmt.Errorf("apptest: application %s is not defined", key)
	}
	dependencies := definition.Dependencies["applications."+key]
	return &service{key: key, application: application, dependencies: dependencies}, nil
}

type service struct {
	key          string
	application  compiler.Application
	dependencies []string
}

func (s *service) Key() string  { return s.key }
func (s *service) Type() string { return "application" }

func (s *service) Dependencies() []string {
	out := make([]string, len(s.dependencies))
	copy(out, s.dependencies)
	sort.Strings(out)
	return out
}

func (s *service) Outputs() []string { return nil }

func (s *service) Artifacts() []module.ArtifactRequirement {
	return []module.ArtifactRequirement{{Application: s.key, Source: s.application.Source}}
}

func (s *service) Steps() []module.Step {
	return []module.Step{
		{Key: "prepare-artifact:" + s.key, Title: "Prepare artifact for " + s.key},
		{Key: "apply:" + s.key, Title: "Apply " + s.key},
		{Key: "verify:" + s.key, Title: "Verify " + s.key},
	}
}

func (s *service) Removal() module.Removal {
	if len(s.application.Volumes) > 0 {
		return module.Removal{
			DataLoss:    true,
			Description: "removes the running application and deletes its persistent volumes",
		}
	}
	return module.Removal{
		DataLoss:    false,
		Description: "removes the running application; no stored data",
	}
}

// Evaluate projects health from the typed workload projection. A stale or
// unknown observation source short-circuits to unknown: stale counts are
// never evaluated as truth.
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
	for _, resource := range observed {
		if resource.Kind != module.KindWorkload || resource.Name != s.key || resource.Workload == nil {
			continue
		}
		desired := resource.Workload.Desired
		if desired < 0 {
			// The autoscaler owns the replica count; adopt its desire when
			// observed, otherwise the count stays unknowable.
			desired = autoscalerDesire(observed, s.key)
		}
		ready := resource.Workload.Ready
		switch {
		case desired <= 0:
			return module.Evaluation{Health: module.HealthUnknown, Diagnostics: []module.Diagnostic{{
				Severity: "warning", Code: "no-replicas",
				Message: "the workload requests zero replicas", Resource: resource.Name,
			}}}
		case ready >= desired:
			return module.Evaluation{Health: module.HealthHealthy}
		case ready == 0:
			return module.Evaluation{Health: module.HealthUnhealthy, Diagnostics: []module.Diagnostic{{
				Severity: "error", Code: "no-ready-replicas",
				Message: fmt.Sprintf("0/%d replicas ready", desired), Resource: resource.Name,
			}}}
		default:
			return module.Evaluation{Health: module.HealthDegraded, Diagnostics: []module.Diagnostic{{
				Severity: "warning", Code: "partial-availability",
				Message: fmt.Sprintf("%d/%d replicas ready", ready, desired), Resource: resource.Name,
			}}}
		}
	}
	return module.Evaluation{Health: module.HealthUnknown, Diagnostics: []module.Diagnostic{{
		Severity: "error", Code: "missing-resource",
		Message: "no workload observed for " + s.key,
	}}}
}

func autoscalerDesire(observed []module.ObservedResource, key string) int32 {
	for _, resource := range observed {
		if resource.Kind == module.KindAutoscaler && resource.Name == key && resource.Autoscaler != nil {
			return resource.Autoscaler.DesiredReplicas
		}
	}
	return 0
}
