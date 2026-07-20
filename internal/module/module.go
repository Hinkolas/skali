// Package module defines the service-module contract from REWORK_V2
// section 6.7: each service kind (application, database, bucket, ...) is a
// module that owns decoding, validation, dependency declaration, artifact
// needs, execution steps, removal consequences, and pure health evaluation.
// The generic deployment machinery knows service keys and these interfaces,
// never service-specific fields; adding a service kind must not change it.
//
// Everything in this package is pure: no store, no cluster, no clock.
package module

import (
	"errors"
	"fmt"
	"sort"

	"github.com/Hinkolas/skali/internal/compiler"
)

// Module decodes services of one type out of the canonical definition.
type Module interface {
	// Type is the module's stable identifier: "application", "database",
	// "bucket", ...
	Type() string
	// Decode extracts and validates one service of this type by key,
	// returning its typed prepared form.
	Decode(definition compiler.ProjectDefinition, key string) (Service, error)
}

// Service is one prepared service instance inside a definition.
type Service interface {
	Key() string
	Type() string
	// Dependencies returns the service keys this service depends on, in
	// "collection.key" form.
	Dependencies() []string
	// Outputs returns the output names this service publishes to others.
	Outputs() []string
	// Artifacts returns the external artifacts to prepare before rollout.
	Artifacts() []ArtifactRequirement
	// Steps returns the execution steps this service contributes to a
	// deployment run, with deterministic keys.
	Steps() []Step
	// Removal describes what removing the service destroys.
	Removal() Removal
	// Evaluate derives health from observed state; pure and total.
	Evaluate(observed []ObservedResource) Evaluation
}

// ArtifactRequirement names one external artifact a service needs.
type ArtifactRequirement struct {
	Application string
	Source      compiler.ApplicationSource
}

// Step is one contributed execution step; Key is deterministic so a
// restarted controller reattaches to it.
type Step struct {
	Key   string
	Title string
}

// Removal describes the consequences of removing a service.
type Removal struct {
	DataLoss    bool
	Description string
}

// Registry holds one module per service type.
type Registry struct {
	modules map[string]Module
}

func NewRegistry() *Registry {
	return &Registry{modules: make(map[string]Module)}
}

func (r *Registry) Register(m Module) error {
	if _, exists := r.modules[m.Type()]; exists {
		return fmt.Errorf("module: type %s is already registered", m.Type())
	}
	r.modules[m.Type()] = m
	return nil
}

func (r *Registry) Get(serviceType string) (Module, bool) {
	m, ok := r.modules[serviceType]
	return m, ok
}

func (r *Registry) Types() []string {
	types := make([]string, 0, len(r.modules))
	for serviceType := range r.modules {
		types = append(types, serviceType)
	}
	sort.Strings(types)
	return types
}

// ErrCycle: the dependency graph contains a cycle.
var ErrCycle = errors.New("module: dependency cycle")

// ErrUnknownReference: a dependency references a service key that does not
// exist in the service set.
var ErrUnknownReference = errors.New("module: unknown dependency reference")

// Order returns the services in dependency-ordered batches: every service
// in a batch depends only on services of earlier batches, so one batch can
// roll out concurrently. Deterministic (sorted within batches); rejects
// cycles and references to unknown services.
func Order(services []string, dependencies map[string][]string) ([][]string, error) {
	known := make(map[string]bool, len(services))
	for _, service := range services {
		known[service] = true
	}
	remaining := make(map[string][]string, len(services))
	for _, service := range services {
		for _, dependency := range dependencies[service] {
			if !known[dependency] {
				return nil, fmt.Errorf("%w: %s -> %s", ErrUnknownReference, service, dependency)
			}
		}
		remaining[service] = dependencies[service]
	}

	var batches [][]string
	placed := make(map[string]bool, len(services))
	for len(placed) < len(known) {
		var batch []string
		for service, deps := range remaining {
			if placed[service] {
				continue
			}
			ready := true
			for _, dependency := range deps {
				if !placed[dependency] {
					ready = false
					break
				}
			}
			if ready {
				batch = append(batch, service)
			}
		}
		if len(batch) == 0 {
			var stuck []string
			for service := range remaining {
				if !placed[service] {
					stuck = append(stuck, service)
				}
			}
			sort.Strings(stuck)
			return nil, fmt.Errorf("%w: %v", ErrCycle, stuck)
		}
		sort.Strings(batch)
		for _, service := range batch {
			placed[service] = true
		}
		batches = append(batches, batch)
	}
	return batches, nil
}
