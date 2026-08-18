// Package bucket is the production object-storage module: the
// service-module contract implementation for manifest buckets backed by the
// platform substrate. It renders no Kubernetes objects itself; the
// substrate controller provisions the store, allocations, and identities,
// and this module's pure evaluation projects the claim's observed state
// into service health.
package bucket

import (
	"fmt"
	"time"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/module"
)

// sourceName selects the provider observation the module opts into by name:
// its staleness degrades bucket health without blocking
// any other module's evaluation.
const sourceName = "seaweedfs"

type Module struct{}

func (Module) Type() string { return "bucket" }

func (Module) Decode(definition compiler.ProjectDefinition, key string) (module.Service, error) {
	bucket, ok := definition.Buckets[key]
	if !ok {
		return nil, fmt.Errorf("bucket: bucket %s is not defined", key)
	}
	return &service{key: key, bucket: bucket}, nil
}

type service struct {
	key    string
	bucket compiler.BucketClaim
}

func (s *service) Key() string  { return s.key }
func (s *service) Type() string { return "bucket" }

// Dependencies: a bucket depends on nothing; applications depend on it.
func (s *service) Dependencies() []string { return nil }

// Outputs mirror the compiler's expression catalog for buckets.
func (s *service) Outputs() []string {
	return []string{"endpoint", "name", "region", "access_key", "secret_key"}
}

func (s *service) Artifacts() []module.ArtifactRequirement { return nil }

func (s *service) Steps() []module.Step {
	return []module.Step{
		{Key: "claim:buckets." + s.key, Title: "Provision buckets." + s.key},
	}
}

func (s *service) Removal() module.Removal {
	return module.Removal{
		DataLoss:    true,
		Description: "deletes the bucket and its objects",
	}
}

// Evaluate projects bucket health from the substrate's claim observation
// plus the provider observer's store and usage projections. Provisioned
// means consumers may bind; the provider source's staleness degrades
// instead of blanking, so a SeaweedFS outage never blocks application
// observation.
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
		if resource.Kind == module.KindBucketClaim && resource.Claim != nil {
			status = resource.Claim
			break
		}
	}
	if status == nil {
		return module.Evaluation{Health: module.HealthUnknown, Diagnostics: []module.Diagnostic{{
			Severity: "warning", Code: "missing-resource",
			Message: "no claim observed for buckets." + s.key,
		}}}
	}

	switch claim.Phase(status.Phase) {
	case claim.PhaseProvisioned:
		return evaluateProvisioned(observed)
	case claim.PhaseReleasing, claim.PhaseReleased:
		return module.Evaluation{Health: module.HealthProgressing, Diagnostics: []module.Diagnostic{{
			Severity: "info", Code: "claim-releasing",
			Message: "the bucket is being released",
		}}}
	default:
		message := status.Waiting
		if message == "" {
			message = "waiting for the object store"
		}
		return module.Evaluation{Health: module.HealthProgressing, Diagnostics: []module.Diagnostic{{
			Severity: "info", Code: "claim-" + status.Phase,
			Message: message,
		}}}
	}
}

// evaluateProvisioned composes a provisioned claim with the observed store
// and per-bucket usage. A stale provider source degrades with the last
// known state retained.
func evaluateProvisioned(observed []module.ObservedResource) module.Evaluation {
	var objectStore *module.ObjectStoreStatus
	for _, resource := range observed {
		if resource.Kind == module.KindObjectStore && resource.ObjectStore != nil {
			objectStore = resource.ObjectStore
			break
		}
	}

	source := module.SourceNamed(observed, sourceName)
	if source == nil {
		// No provider observer registered: claim truth is all there is.
		return module.Evaluation{Health: module.HealthHealthy}
	}
	if source.State != module.SourceFresh {
		message := "the object-store observation is " + source.State
		if !source.StaleSince.IsZero() {
			message = "the object-store observation is stale since " + source.StaleSince.UTC().Format(time.RFC3339)
		}
		return module.Evaluation{Health: module.HealthDegraded, Diagnostics: []module.Diagnostic{{
			Severity: "warning", Code: "observation_stale_since", Message: message,
		}}}
	}

	if objectStore != nil && !objectStore.S3Ready {
		return module.Evaluation{Health: module.HealthUnhealthy, Diagnostics: []module.Diagnostic{{
			Severity: "error", Code: "store-unavailable",
			Message: "the object store's S3 gateway is not serving",
		}}}
	}

	var bucket *module.BucketStatus
	for _, resource := range observed {
		if resource.Kind == module.KindBucket && resource.Bucket != nil {
			bucket = resource.Bucket
			break
		}
	}
	switch {
	case bucket == nil:
		return progressing("bucket-unobserved", "waiting for the bucket observation to catch up")
	case !bucket.Exists:
		return progressing("bucket-missing", "the bucket is not visible on the store yet")
	case bucket.ReadOnly:
		return module.Evaluation{Health: module.HealthDegraded, Diagnostics: []module.Diagnostic{{
			Severity: "warning", Code: "quota-exceeded",
			Message: fmt.Sprintf("storage quota reached (%d of %d bytes used); the bucket is read-only until space is freed",
				bucket.UsedBytes, bucket.QuotaBytes),
		}}}
	}
	diagnostics := []module.Diagnostic{}
	if bucket.QuotaBytes > 0 {
		diagnostics = append(diagnostics, module.Diagnostic{
			Severity: "info", Code: "usage",
			Message: fmt.Sprintf("%d objects, %d of %d bytes used",
				bucket.ObjectCount, bucket.UsedBytes, bucket.QuotaBytes),
		})
	}
	return module.Evaluation{Health: module.HealthHealthy, Diagnostics: diagnostics}
}

func progressing(code, message string) module.Evaluation {
	return module.Evaluation{Health: module.HealthProgressing, Diagnostics: []module.Diagnostic{{
		Severity: "info", Code: code, Message: message,
	}}}
}
