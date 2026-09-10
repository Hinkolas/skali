package reconcile

import (
	"context"
	"fmt"
	"github.com/Hinkolas/skali/internal/kube"
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

const certName = "tls-demo-web-public-5c59ce82ae9851442567672b334b1998"

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
	require.Contains(t, recorded, "apply IngressRoute/"+f.namespace+"/route-demo-web-public-primary-84d18c2ac3f6fedc1433f38e899e23bf")
	require.Contains(t, recorded, "apply IngressRoute/"+f.namespace+"/route-demo-web-public-http-5f1b04c211d4f06c08a014badc51a3fb")
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

// A failed attempt in this rollout whose backoff outlives the budget fails
// on the TLS checkpoint, rather than consuming the entire rollout timeout.
func TestCertificateBackoffFailsPromptly(t *testing.T) {
	f := certFixture(t, Config{RolloutDeadline: 10 * time.Minute})
	ctx := context.Background()
	result := f.executeDeploymentManifest(t, certManifest)
	f.fake.SetFresh()
	f.markHealthy(t)
	failure := f.target(t).UpdatedAt.Add(time.Second)
	f.fake.SetCertificate(f.environmentID, f.namespace, certName, "web", module.CertificateStatus{
		FailedAttempts: 2, LastFailureTime: failure, NextRetryTime: failure.Add(2 * time.Hour), Reason: "Failed", Message: "ACME order invalid: DNS points at the wrong IP", DNSNames: []string{"app.example.com"},
	})
	requeue, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, requeueHealthCheck, requeue)
	run, err := f.st.GetRunByID(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "failed", run.Status)
	step, found, err := f.kernel.deps.Journal.FindStep(ctx, result.RunID, "tls:"+certName)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "failed", step.Status)
	message, err := f.kernel.deps.Journal.LatestStepMessage(ctx, step.ID)
	require.NoError(t, err)
	require.Contains(t, message, "failed attempts: 2")
	require.Contains(t, message, "next attempt: 3")
	require.Contains(t, message, "Next automatic retry is after the rollout deadline")
	require.Contains(t, message, "DNS points at the wrong IP")
	require.Equal(t, result.RevisionID, *f.target(t).TargetRevisionID, "first deploy keeps converging after the run fails")
}

func TestCertificateRedeployRecoveryAndCheckpoint(t *testing.T) {
	f := certFixture(t, Config{RolloutDeadline: 10 * time.Minute})
	ctx := context.Background()
	result := f.executeDeploymentManifest(t, certManifest)
	f.fake.SetFresh()
	f.markHealthy(t)
	failure := f.target(t).UpdatedAt.Add(-time.Hour)
	failing := module.CertificateStatus{FailedAttempts: 1, LastFailureTime: failure, NextRetryTime: failure.Add(time.Hour), Reason: "Failed", Message: "old order invalid", DNSNames: []string{"app.example.com"}}
	f.fake.SetCertificate(f.environmentID, f.namespace, certName, "web", failing)
	retries := 0
	f.kernel.deps.RetryCertificate = func(_ context.Context, ref kube.ObjectRef, promoted time.Time) (bool, error) {
		require.Equal(t, certName, ref.Name)
		require.Equal(t, f.target(t).UpdatedAt, promoted)
		retries++
		issuing := failing
		issuing.Issuing = true
		issuing.NextRetryTime = time.Time{}
		issuing.Reason = "ManuallyTriggered"
		f.fake.SetCertificate(f.environmentID, f.namespace, certName, "web", issuing)
		return true, nil
	}
	for i := 0; i < 3; i++ {
		_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
		require.NoError(t, err)
	}
	require.Equal(t, 1, retries)
	run, err := f.st.GetRunByID(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "running", run.Status)
	step, found, err := f.kernel.deps.Journal.FindStep(ctx, result.RunID, "tls:"+certName)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "waiting", step.Status)
	message, err := f.kernel.deps.Journal.LatestStepMessage(ctx, step.ID)
	require.NoError(t, err)
	require.Contains(t, message, "attempt 2")
	// A successful issuance closes its own checkpoint and activates.
	f.fake.SetCertificate(f.environmentID, f.namespace, certName, "web", module.CertificateStatus{Ready: true, NotAfter: time.Now().Add(time.Hour)})
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	step, _, err = f.kernel.deps.Journal.FindStep(ctx, result.RunID, "tls:"+certName)
	require.NoError(t, err)
	require.Equal(t, "succeeded", step.Status)
	run, err = f.st.GetRunByID(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", run.Status)
}

func TestCertificateInspectionFailureDoesNotFailEarly(t *testing.T) {
	f := certFixture(t, Config{RolloutDeadline: 10 * time.Minute})
	ctx := context.Background()
	result := f.executeDeploymentManifest(t, certManifest)
	f.fake.SetFresh()
	f.markHealthy(t)
	failure := f.target(t).UpdatedAt.Add(time.Second)
	cert := module.CertificateStatus{FailedAttempts: 1, LastFailureTime: failure, NextRetryTime: failure.Add(time.Hour)}
	f.fake.SetCertificate(f.environmentID, f.namespace, certName, "web", cert)
	f.kernel.deps.InspectCertificate = func(context.Context, kube.ObjectRef) (*module.CertificateStatus, map[string]string, error) {
		return &cert, nil, fmt.Errorf("orders: forbidden")
	}
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	run, err := f.st.GetRunByID(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "running", run.Status)
	step, _, err := f.kernel.deps.Journal.FindStep(ctx, result.RunID, "tls:"+certName)
	require.NoError(t, err)
	message, err := f.kernel.deps.Journal.LatestStepMessage(ctx, step.ID)
	require.NoError(t, err)
	require.Contains(t, message, "orders: forbidden")
}

func TestCertificateRecoveryDoesNotDependOnJournal(t *testing.T) {
	f := certFixture(t, Config{RolloutDeadline: 10 * time.Minute})
	ctx := context.Background()
	f.executeDeploymentManifest(t, certManifest)
	f.fake.SetFresh()
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	_, err = f.st.Pool.Exec(ctx, "DELETE FROM runs")
	require.NoError(t, err)
	failure := f.target(t).UpdatedAt.Add(-time.Hour)
	f.fake.SetCertificate(f.environmentID, f.namespace, certName, "web", module.CertificateStatus{LastFailureTime: failure, FailedAttempts: 1})
	retries := 0
	f.kernel.deps.RetryCertificate = func(context.Context, kube.ObjectRef, time.Time) (bool, error) { retries++; return true, nil }
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, 1, retries)
}
