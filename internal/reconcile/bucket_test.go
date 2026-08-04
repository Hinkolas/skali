package reconcile

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/module"
)

const bucketManifest = `version: "1"
name: demo
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
      S3_ENDPOINT: "{{ buckets.files.endpoint }}"
      S3_SECRET_KEY: "{{ buckets.files.secret_key }}"
buckets:
  files:
    quotas:
      storage: 1GB
`

// A bucket-bearing revision traverses the same generic machinery as
// databases: the application waits visibly on the claim, provisioning
// unblocks it, and activation requires both healthy. Zero kernel branches
// on the service kind (section 6.7).
func TestReconcileBucketClaimGatesApplication(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	claims := &fakeClaims{states: []ClaimState{{
		Service: "buckets.files", Waiting: "waiting for the object store",
	}}}
	f.kernel.deps.Claims = claims
	claimID := uuid.Must(uuid.NewV7())

	result := f.executeDeploymentManifest(t, bucketManifest)
	f.fake.SetFresh()

	// Pass 1: the claim is pending; the application must not apply.
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, 1, claims.calls)
	require.NotContains(t, f.cluster.recorded(), "apply Deployment/"+f.namespace+"/demo-web",
		"the application must wait for the bucket claim")

	tree, err := f.journal.RunTree(ctx, result.RunID)
	require.NoError(t, err)
	steps := flattenSteps(tree.Steps)
	require.Equal(t, "waiting", steps["claim:buckets.files"])
	require.Equal(t, "waiting", steps["apply:web"])

	// The substrate provisions: readiness and the claim projection flip
	// together, and the provider observer reports the bucket on the store.
	claims.states = []ClaimState{{Service: "buckets.files", Provisioned: true}}
	f.fake.SetBucketClaim(f.environmentID, "buckets.files", claimID,
		module.ClaimStatus{Phase: "provisioned"})
	f.fake.SetSourceFresh("seaweedfs")
	f.fake.SetObjectStore("seaweed",
		module.ObjectStoreStatus{MastersDesired: 1, MastersReady: 1, FilerReady: true, S3Ready: true})
	f.fake.SetBucketUsage(f.environmentID, "buckets.files", "objectstore/seaweed", "b-files-01",
		module.BucketStatus{Exists: true, QuotaBytes: 1 << 30})

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
	require.Equal(t, "succeeded", steps["claim:buckets.files"])
	require.Equal(t, "succeeded", steps["apply:web"])
	require.Equal(t, "succeeded", steps["activate"])

	// The status projection carries the bucket with its type and health.
	status, err := f.kernel.Status(ctx, f.environmentID)
	require.NoError(t, err)
	health := map[string]module.Health{}
	for _, service := range status.Services {
		health[service.Type+"."+service.Key] = service.Health
	}
	require.Equal(t, module.HealthHealthy, health["bucket.files"])
	require.Equal(t, module.HealthHealthy, health["application.web"])
}

// Without a substrate (API-only mode) bucket services wait visibly instead
// of failing the pass.
func TestReconcileBucketWithoutSubstrateWaits(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	result := f.executeDeploymentManifest(t, bucketManifest)
	f.fake.SetFresh()

	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.NotContains(t, f.cluster.recorded(), "apply Deployment/"+f.namespace+"/demo-web")

	tree, err := f.journal.RunTree(ctx, result.RunID)
	require.NoError(t, err)
	steps := flattenSteps(tree.Steps)
	require.Equal(t, "waiting", steps["claim:buckets.files"])
}
