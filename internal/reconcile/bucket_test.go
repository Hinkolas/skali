package reconcile

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"

	rendering "github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/module"
)

const bucketManifest = `name: demo
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
// on the service kind.
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
	require.NotContains(t, f.cluster.recorded(), "apply Deployment/"+f.namespace+"/"+f.webDeploymentName(t),
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
	require.Contains(t, f.cluster.recorded(), "apply Deployment/"+f.namespace+"/"+f.webDeploymentName(t))
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
	require.NotContains(t, f.cluster.recorded(), "apply Deployment/"+f.namespace+"/"+f.webDeploymentName(t))

	tree, err := f.journal.RunTree(ctx, result.RunID)
	require.NoError(t, err)
	steps := flattenSteps(tree.Steps)
	require.Equal(t, "waiting", steps["claim:buckets.files"])
}

const rotationManifest = `name: demo
applications:
  web:
    image: ghcr.io/example/web:1.0.0
    ports:
      http:
        port: 8080
        protocol: http
    environment:
      S3_ENDPOINT: "{{ buckets.files.endpoint }}"
      S3_SECRET_KEY: "{{ buckets.files.secret_key }}"
  static:
    image: ghcr.io/example/static:1.0.0
    ports:
      http:
        port: 8080
        protocol: http
buckets:
  files:
    quotas:
      storage: 1GB
`

// deploymentName is the rendered Deployment name of one application for
// the current target revision, color included.
func (f *kernelFixture) deploymentName(t *testing.T, application string) string {
	t.Helper()
	ctx := context.Background()
	target := f.target(t)
	require.NotNil(t, target.TargetRevisionID)
	rev, err := f.deploy.GetRevision(ctx, *target.TargetRevisionID)
	require.NoError(t, err)
	intercepts, err := f.kernel.loadIntercepts(ctx, f.environmentID)
	require.NoError(t, err)
	colors, err := f.kernel.desiredColors(ctx, f.environmentID, target, rev, intercepts)
	require.NoError(t, err)
	if color, ok := colors[application]; ok {
		return rendering.ColoredApplicationName("demo", application, color)
	}
	return rendering.ApplicationName("demo", application)
}

// setWorkload records one application's Deployment for the current target
// revision as fully available, under its rendered name and color.
func (f *kernelFixture) setWorkload(t *testing.T, application string) {
	t.Helper()
	ctx := context.Background()
	target := f.target(t)
	row, err := f.st.GetRevisionByID(ctx, *target.TargetRevisionID)
	require.NoError(t, err)
	rev, err := f.deploy.GetRevision(ctx, *target.TargetRevisionID)
	require.NoError(t, err)
	intercepts, err := f.kernel.loadIntercepts(ctx, f.environmentID)
	require.NoError(t, err)
	colors, err := f.kernel.desiredColors(ctx, f.environmentID, target, rev, intercepts)
	require.NoError(t, err)
	f.fake.SetColoredWorkload(f.environmentID, f.namespace, f.deploymentName(t, application), application,
		row.Checksum[:16], colors[application], module.WorkloadStatus{Desired: 1, Ready: 1, Updated: 1})
}

// valuesHash reads the values identity the kernel stamped onto the last
// applied Deployment of one application.
func (f *kernelFixture) valuesHash(t *testing.T, application string) (string, string) {
	t.Helper()
	name := f.deploymentName(t, application)
	obj := f.cluster.lastApplied("Deployment/" + f.namespace + "/" + name)
	require.NotNil(t, obj, "no Deployment applied for %s", application)
	deployment, ok := obj.(*appsv1.Deployment)
	require.True(t, ok)
	return name, deployment.Spec.Template.Annotations[rendering.AnnotationValuesHash]
}

// A changed output generation (a rotated credential) reaches exactly the
// applications referencing the service: the consumer's pod template
// identity changes and it rolls to a new color, the bystander's does not.
func TestReconcileOutputGenerationRollsConsumers(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	claims := &fakeClaims{
		states:      []ClaimState{{Service: "buckets.files", Provisioned: true}},
		generations: map[string]string{"buckets.files": "aaaaaaaaaaaaaaaa"},
	}
	f.kernel.deps.Claims = claims
	f.executeDeploymentManifest(t, rotationManifest)
	f.fake.SetFresh()
	f.fake.SetBucketClaim(f.environmentID, "buckets.files", uuid.Must(uuid.NewV7()),
		module.ClaimStatus{Phase: "provisioned"})
	f.fake.SetSourceFresh("seaweedfs")
	f.fake.SetObjectStore("seaweed",
		module.ObjectStoreStatus{MastersDesired: 1, MastersReady: 1, FilerReady: true, S3Ready: true})
	f.fake.SetBucketUsage(f.environmentID, "buckets.files", "objectstore/seaweed", "b-files-01",
		module.BucketStatus{Exists: true, QuotaBytes: 1 << 30})

	// The first pass records the claim; the consumer applies on the next,
	// the first whose generations describe a provisioned claim.
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	// The bystander applies first and must be healthy before the next
	// batch (the consumer, behind its claim) applies.
	f.setWorkload(t, "static")
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	f.setWorkload(t, "web")
	webBefore, webHashBefore := f.valuesHash(t, "web")
	staticBefore, staticHashBefore := f.valuesHash(t, "static")
	require.NotEmpty(t, webHashBefore, "a consumer carries the service generation in its identity")
	require.Empty(t, staticHashBefore, "a bystander references no value and no service")

	// The substrate bumps the credential version: a new generation.
	claims.generations = map[string]string{"buckets.files": "bbbbbbbbbbbbbbbb"}
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	webAfter, webHashAfter := f.valuesHash(t, "web")
	staticAfter, staticHashAfter := f.valuesHash(t, "static")
	require.NotEqual(t, webHashBefore, webHashAfter, "the consumer's pod template identity follows the generation")
	require.NotEqual(t, webBefore, webAfter, "a blue-green consumer rolls to its other color")
	require.Equal(t, staticHashBefore, staticHashAfter)
	require.Equal(t, staticBefore, staticAfter, "the bystander keeps its workload")
}
