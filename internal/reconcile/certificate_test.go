package reconcile

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/module/app"
	"github.com/Hinkolas/skali/internal/module/bucket"
	"github.com/Hinkolas/skali/internal/module/database"
)

// certManifest declares one automatic-TLS route; on a certificate-capable
// installation its issuance gates activation.
const certManifest = `version: "1"
name: demo
applications:
  web:
    image: ghcr.io/example/web:1.0.0
    ports:
      http:
        port: 8080
        protocol: http
    routes:
      public:
        domain: "${APP_DOMAIN}"
        port: http
    environment:
      APP_DOMAIN: "${APP_DOMAIN}"
      SESSION_SECRET: "${SESSION_SECRET}"
`

// certFixture is the kernel fixture flipped to a certificate-capable
// installation: the app module gates on issuance and rendering emits the
// edge objects.
func certFixture(t *testing.T, cfg Config) *kernelFixture {
	t.Helper()
	cfg.Certificates = true
	f := newKernelFixture(t, cfg)
	registry := module.NewRegistry()
	require.NoError(t, registry.Register(app.Module{Certificates: true}))
	require.NoError(t, registry.Register(database.Module{}))
	require.NoError(t, registry.Register(bucket.Module{}))
	f.kernel.deps.Registry = registry
	return f
}

const certName = "demo-web-public-tls"

// The certificate gate end to end: a healthy workload does not activate
// while issuance is pending, the rollout deadline fails the run with the
// certificate reason (keeping a first deployment's target), and a late
// issuance still activates.
func TestCertificateGatesActivation(t *testing.T) {
	t.Parallel()
	f := certFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()

	result := f.executeDeploymentManifest(t, certManifest)
	f.fake.SetFresh()
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)

	// The desired set carries the edge objects.
	recorded := f.cluster.recorded()
	require.Contains(t, recorded, "apply IngressRoute/"+f.namespace+"/demo-web-public")
	require.Contains(t, recorded, "apply IngressRoute/"+f.namespace+"/demo-web-public-http")
	require.Contains(t, recorded, "apply Certificate/"+f.namespace+"/"+certName)
	require.Contains(t, recorded, "apply Middleware/"+f.namespace+"/redirect-https")

	// Healthy workload, certificate pending: no activation.
	f.markHealthy(t)
	f.fake.SetCertificate(f.environmentID, f.namespace, certName, "web",
		module.CertificateStatus{Issuing: true, Reason: "Pending",
			Message: "waiting for the ACME challenge"})
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Nil(t, f.target(t).ActiveRevisionID,
		"a pending certificate must hold activation")
	run, err := f.st.GetRunByID(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "running", run.Status)

	// Past the deadline the run fails naming the certificate; the first
	// deployment has nothing to fall back to and keeps its target on the
	// health-check cadence.
	f.kernel.cfg.RolloutDeadline = time.Nanosecond
	requeue, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, requeueHealthCheck, requeue)
	run, err = f.st.GetRunByID(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "failed", run.Status)
	target := f.target(t)
	require.Equal(t, result.RevisionID, *target.TargetRevisionID)
	require.Nil(t, target.ActiveRevisionID)

	// Late issuance still activates the level-triggered target.
	f.kernel.cfg.RolloutDeadline = time.Hour
	f.fake.SetCertificate(f.environmentID, f.namespace, certName, "web",
		module.CertificateStatus{Ready: true, NotAfter: time.Now().Add(60 * 24 * time.Hour)})
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.NotNil(t, f.target(t).ActiveRevisionID)
	require.Equal(t, result.RevisionID, *f.target(t).ActiveRevisionID)
}

// A redeploy whose only trouble is a failing renewal of a still-valid
// certificate must activate: post-issuance certificate state warns, never
// gates. The rollback interplay rides on this: falling back to a revision
// desiring the same certificate would not help, so blocking would wedge.
func TestCertificateRenewalDoesNotBlock(t *testing.T) {
	t.Parallel()
	f := certFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()

	// Revision A activates with an issued certificate.
	f.executeDeploymentManifest(t, certManifest)
	f.fake.SetFresh()
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	f.markHealthy(t)
	f.fake.SetCertificate(f.environmentID, f.namespace, certName, "web",
		module.CertificateStatus{Ready: true, NotAfter: time.Now().Add(60 * 24 * time.Hour)})
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.NotNil(t, f.target(t).ActiveRevisionID)

	// The renewal starts failing while the certificate is still valid;
	// revision B must roll out and activate regardless.
	f.fake.SetCertificate(f.environmentID, f.namespace, certName, "web",
		module.CertificateStatus{Ready: false, Reason: "Failed",
			Message:  "ACME authorization failed",
			NotAfter: time.Now().Add(10 * 24 * time.Hour), FailedAttempts: 1})
	second := f.deployChanged(t, certManifest)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	f.markHealthy(t)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	run, err := f.st.GetRunByID(ctx, second.RunID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", run.Status,
		"a failing renewal of a valid certificate must never fail a deploy")
	require.Equal(t, second.RevisionID, *f.target(t).ActiveRevisionID)
}
