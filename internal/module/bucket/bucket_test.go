package bucket

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/module"
)

func definition() compiler.ProjectDefinition {
	return compiler.ProjectDefinition{
		Buckets: map[string]compiler.BucketClaim{
			"files": {Visibility: "private", StorageQuotaBytes: 1 << 30, Versioning: "disabled"},
		},
	}
}

func TestDecodeAndContract(t *testing.T) {
	t.Parallel()
	service, err := Module{}.Decode(definition(), "files")
	require.NoError(t, err)
	require.Equal(t, "files", service.Key())
	require.Equal(t, "bucket", service.Type())
	require.Nil(t, service.Dependencies())
	require.Equal(t, []string{"endpoint", "name", "region", "access_key", "secret_key"}, service.Outputs())
	require.Nil(t, service.Artifacts())
	require.Equal(t, "claim:buckets.files", service.Steps()[0].Key)
	require.True(t, service.Removal().DataLoss)

	_, err = Module{}.Decode(definition(), "missing")
	require.Error(t, err)
}

func fresh() module.ObservedResource {
	return module.ObservedResource{
		Kind: module.KindSource, Name: "kubernetes",
		Source: &module.SourceStatus{State: module.SourceFresh},
	}
}

func seaweedSource(state string) module.ObservedResource {
	source := &module.SourceStatus{State: state}
	if state == module.SourceStale {
		source.StaleSince = time.Unix(1700000000, 0)
	}
	return module.ObservedResource{Kind: module.KindSource, Name: "seaweedfs", Source: source}
}

func claimResource(phase, waiting string) module.ObservedResource {
	return module.ObservedResource{
		Kind:  module.KindBucketClaim,
		Claim: &module.ClaimStatus{Phase: phase, Waiting: waiting},
	}
}

func TestEvaluate(t *testing.T) {
	t.Parallel()
	service, err := Module{}.Decode(definition(), "files")
	require.NoError(t, err)

	// A stale cluster view blanks like every module.
	stale := service.Evaluate([]module.ObservedResource{{
		Kind: module.KindSource, Name: "kubernetes",
		Source: &module.SourceStatus{State: module.SourceStale},
	}})
	require.Equal(t, module.HealthUnknown, stale.Health)

	// No claim projection at all.
	missing := service.Evaluate([]module.ObservedResource{fresh()})
	require.Equal(t, module.HealthUnknown, missing.Health)

	// Pending surfaces the substrate's waiting reason.
	pending := service.Evaluate([]module.ObservedResource{fresh(),
		claimResource("pending", "waiting for the object store")})
	require.Equal(t, module.HealthProgressing, pending.Health)
	require.Contains(t, pending.Diagnostics[0].Message, "object store")

	// Provisioned with no provider observer registered: claim truth only.
	bare := service.Evaluate([]module.ObservedResource{fresh(), claimResource("provisioned", "")})
	require.Equal(t, module.HealthHealthy, bare.Health)

	// The full provider view: healthy with a usage diagnostic.
	usage := module.ObservedResource{Kind: module.KindBucket,
		Bucket: &module.BucketStatus{Exists: true, UsedBytes: 100, ObjectCount: 3, QuotaBytes: 1 << 30}}
	store := module.ObservedResource{Kind: module.KindObjectStore,
		ObjectStore: &module.ObjectStoreStatus{MastersReady: 1, FilerReady: true, S3Ready: true}}
	healthy := service.Evaluate([]module.ObservedResource{fresh(),
		seaweedSource(module.SourceFresh), claimResource("provisioned", ""), store, usage})
	require.Equal(t, module.HealthHealthy, healthy.Health)
	require.Equal(t, "usage", healthy.Diagnostics[0].Code)

	// A stale provider degrades without blanking (the R6 exit criterion).
	degraded := service.Evaluate([]module.ObservedResource{fresh(),
		seaweedSource(module.SourceStale), claimResource("provisioned", ""), store, usage})
	require.Equal(t, module.HealthDegraded, degraded.Health)
	require.Equal(t, "observation_stale_since", degraded.Diagnostics[0].Code)

	// Quota exhaustion flips the bucket read-only: degraded, explained.
	full := usage
	full.Bucket = &module.BucketStatus{Exists: true, UsedBytes: 1 << 30, QuotaBytes: 1 << 30, ReadOnly: true}
	quota := service.Evaluate([]module.ObservedResource{fresh(),
		seaweedSource(module.SourceFresh), claimResource("provisioned", ""), store, full})
	require.Equal(t, module.HealthDegraded, quota.Health)
	require.Equal(t, "quota-exceeded", quota.Diagnostics[0].Code)

	// A gateway outage is unhealthy.
	down := module.ObservedResource{Kind: module.KindObjectStore,
		ObjectStore: &module.ObjectStoreStatus{MastersReady: 1, FilerReady: false, S3Ready: false}}
	unhealthy := service.Evaluate([]module.ObservedResource{fresh(),
		seaweedSource(module.SourceFresh), claimResource("provisioned", ""), down, usage})
	require.Equal(t, module.HealthUnhealthy, unhealthy.Health)

	// Releasing reflects the persisted destructive decision.
	releasing := service.Evaluate([]module.ObservedResource{fresh(), claimResource("releasing", "")})
	require.Equal(t, module.HealthProgressing, releasing.Health)
	require.Equal(t, "claim-releasing", releasing.Diagnostics[0].Code)
}
