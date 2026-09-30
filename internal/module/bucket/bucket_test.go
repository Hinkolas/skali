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
	require.Equal(t, []string{"endpoint", "internal_endpoint", "name", "region", "access_key", "secret_key"}, service.Outputs())
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
		Bucket: &module.BucketStatus{Exists: true, UsedBytes: 100, EntryCount: 3, QuotaBytes: 1 << 30}}
	store := module.ObservedResource{Kind: module.KindObjectStore,
		ObjectStore: &module.ObjectStoreStatus{MastersReady: 1, FilerReady: true, S3Ready: true}}
	healthy := service.Evaluate([]module.ObservedResource{fresh(),
		seaweedSource(module.SourceFresh), claimResource("provisioned", ""), store, usage})
	require.Equal(t, module.HealthHealthy, healthy.Health)
	require.Equal(t, "usage", healthy.Diagnostics[0].Code)
	require.Equal(t, "100 of 1073741824 bytes used; about 3 stored entries", healthy.Diagnostics[0].Message,
		"entries, not objects: a large object is several entries")

	// A stale provider degrades without blanking.
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

	// Settings reset by the provider stay healthy but leave a warning: the
	// audit trail of something having changed the bucket behind skali.
	drifted := usage
	drifted.Bucket = &module.BucketStatus{Exists: true, UsedBytes: 100, QuotaBytes: 1 << 30,
		ConfigurationDrift: []string{"policy", "cors"}}
	drift := service.Evaluate([]module.ObservedResource{fresh(),
		seaweedSource(module.SourceFresh), claimResource("provisioned", ""), store, drifted})
	require.Equal(t, module.HealthHealthy, drift.Health)
	require.Equal(t, "configuration-drift", drift.Diagnostics[0].Code)
	require.Equal(t, "warning", drift.Diagnostics[0].Severity)
	require.Contains(t, drift.Diagnostics[0].Message, "policy, cors")

	// A gateway outage is unhealthy.
	down := module.ObservedResource{Kind: module.KindObjectStore,
		ObjectStore: &module.ObjectStoreStatus{MastersReady: 1, FilerReady: false, S3Ready: false}}
	unhealthy := service.Evaluate([]module.ObservedResource{fresh(),
		seaweedSource(module.SourceFresh), claimResource("provisioned", ""), down, usage})
	require.Equal(t, module.HealthUnhealthy, unhealthy.Health)

	// The gateway's own explanation travels with the outage.
	explained := module.ObservedResource{Kind: module.KindObjectStore,
		ObjectStore: &module.ObjectStoreStatus{MastersReady: 1, FilerReady: true, S3Ready: false,
			S3Detail: "the S3 gateway serves anonymous requests"}}
	unhealthy = service.Evaluate([]module.ObservedResource{fresh(),
		seaweedSource(module.SourceFresh), claimResource("provisioned", ""), explained, usage})
	require.Equal(t, module.HealthUnhealthy, unhealthy.Health)
	require.Contains(t, unhealthy.Diagnostics[0].Message, "anonymous")
	// No volume server at all: nothing can be read or written.
	noVolumes := module.ObservedResource{Kind: module.KindObjectStore,
		ObjectStore: &module.ObjectStoreStatus{MastersDesired: 1, MastersReady: 1, VolumeServersDesired: 2,
			VolumeServersReady: 0, FilerReady: true, S3Ready: true}}
	unhealthy = service.Evaluate([]module.ObservedResource{fresh(),
		seaweedSource(module.SourceFresh), claimResource("provisioned", ""), noVolumes, usage})
	require.Equal(t, module.HealthUnhealthy, unhealthy.Health)
	require.Contains(t, unhealthy.Diagnostics[0].Message, "no volume server")
	// Below the recorded shape (a master and a volume server down, copies
	// missing): still serving, degraded, each shortfall named.
	short := module.ObservedResource{Kind: module.KindObjectStore,
		ObjectStore: &module.ObjectStoreStatus{MastersDesired: 3, MastersReady: 2, VolumeServersDesired: 3,
			VolumeServersReady: 2, UnderReplicatedVolumes: 4, FilerReady: true, S3Ready: true}}
	degraded = service.Evaluate([]module.ObservedResource{fresh(),
		seaweedSource(module.SourceFresh), claimResource("provisioned", ""), short, usage})
	require.Equal(t, module.HealthDegraded, degraded.Health)
	codes := []string{}
	for _, diagnostic := range degraded.Diagnostics {
		codes = append(codes, diagnostic.Code)
	}
	require.Equal(t, []string{"store-degraded", "store-degraded", "store-degraded", "usage"}, codes)
	require.Contains(t, degraded.Diagnostics[0].Message, "2 of 3 object-store masters")
	require.Contains(t, degraded.Diagnostics[1].Message, "2 of 3 volume servers")
	require.Contains(t, degraded.Diagnostics[2].Message, "4 volumes have fewer copies")
	// A public endpoint whose certificate is not issued degrades and says
	// what that means for presigned URLs; an issued one is silent.
	certPending := module.ObservedResource{Kind: module.KindObjectStore,
		ObjectStore: &module.ObjectStoreStatus{MastersDesired: 1, MastersReady: 1, VolumeServersDesired: 1,
			VolumeServersReady: 1, FilerReady: true, S3Ready: true,
			PublicEndpoint: &module.PublicEndpointStatus{Domain: "s3.example.com",
				Certificate: &module.CertificateStatus{Ready: false, Message: "waiting for HTTP-01"}}}}
	degraded = service.Evaluate([]module.ObservedResource{fresh(),
		seaweedSource(module.SourceFresh), claimResource("provisioned", ""), certPending, usage})
	require.Equal(t, module.HealthDegraded, degraded.Health)
	require.Equal(t, "endpoint-certificate", degraded.Diagnostics[0].Code)
	require.Contains(t, degraded.Diagnostics[0].Message, "s3.example.com")
	require.Contains(t, degraded.Diagnostics[0].Message, "waiting for HTTP-01")
	certIssued := module.ObservedResource{Kind: module.KindObjectStore,
		ObjectStore: &module.ObjectStoreStatus{MastersDesired: 1, MastersReady: 1, VolumeServersDesired: 1,
			VolumeServersReady: 1, FilerReady: true, S3Ready: true,
			PublicEndpoint: &module.PublicEndpointStatus{Domain: "s3.example.com", Certificate: &module.CertificateStatus{Ready: true}}}}
	healthy = service.Evaluate([]module.ObservedResource{fresh(),
		seaweedSource(module.SourceFresh), claimResource("provisioned", ""), certIssued, usage})
	require.Equal(t, module.HealthHealthy, healthy.Health)
	require.Equal(t, "usage", healthy.Diagnostics[0].Code)
	// Releasing reflects the persisted destructive decision.
	releasing := service.Evaluate([]module.ObservedResource{fresh(), claimResource("releasing", "")})
	require.Equal(t, module.HealthProgressing, releasing.Health)
	require.Equal(t, "claim-releasing", releasing.Diagnostics[0].Code)
}
