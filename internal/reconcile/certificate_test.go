package reconcile

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/deploy"
	"github.com/Hinkolas/skali/internal/edge/edgeprobe"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/module/app"
	"github.com/Hinkolas/skali/internal/module/bucket"
	"github.com/Hinkolas/skali/internal/module/database"
	"github.com/Hinkolas/skali/internal/store"
)

// certManifest declares one automatic-TLS route; on a certificate-capable
// installation its issuance gates activation.
const certManifest = `skali: v0.1.0-rc.3
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
	require.Contains(t, recorded, "apply Middleware/"+f.namespace+"/compress")

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

// probeResult is a ProbeDomain stub answering one fixed verdict and
// counting its calls.
func probeResult(calls *atomic.Int32, state edgeprobe.State, addresses ...edgeprobe.AddressResult) func(context.Context, string) edgeprobe.Result {
	return func(_ context.Context, domain string) edgeprobe.Result {
		if calls != nil {
			calls.Add(1)
		}
		return edgeprobe.Result{Domain: domain, State: state, Addresses: addresses,
			Message: "stub verdict: " + string(state), CheckedAt: time.Now()}
	}
}

var (
	foreignAddress = edgeprobe.AddressResult{Address: "203.0.113.9", Outcome: edgeprobe.OutcomeForeign, Detail: "HTTP 301 without Skali-Instance"}
	oursAddress    = edgeprobe.AddressResult{Address: "198.51.100.7", Outcome: edgeprobe.OutcomeOurs, Detail: "HTTP 200"}
)

// ageDomainProbe backdates the cached verdict so the next pass probes again.
func (f *kernelFixture) ageDomainProbe(domain string) {
	f.kernel.domainMu.Lock()
	defer f.kernel.domainMu.Unlock()
	entry := f.kernel.domains[domain]
	entry.result.CheckedAt = time.Now().Add(-time.Hour)
	f.kernel.domains[domain] = entry
}

// deployDeferred runs the deferred path to activation: the route domain
// does not reach this edge, so a healthy workload activates with the TLS
// checkpoint skipped.
func (f *kernelFixture) deployDeferred(t *testing.T, state edgeprobe.State, addresses ...edgeprobe.AddressResult) (*deploy.ExecuteResult, time.Duration) {
	t.Helper()
	ctx := context.Background()
	f.kernel.deps.ProbeDomain = probeResult(nil, state, addresses...)
	result := f.executeDeploymentManifest(t, certManifest)
	f.fake.SetFresh()
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	f.markHealthy(t)
	f.fake.SetCertificate(f.environmentID, f.namespace, certName, "web",
		module.CertificateStatus{Issuing: true, Reason: "Pending", Message: "waiting for the ACME challenge"})
	requeue, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	return result, requeue
}

// A domain that does not reach this edge defers the certificate: the
// rollout activates, the run succeeds, the checkpoint ends skipped with the
// verdict, and the status projection carries the edge state and a warning.
func TestCertificateDeferredDomainActivates(t *testing.T) {
	t.Parallel()
	f := certFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	retries := 0
	f.kernel.deps.RetryCertificate = func(context.Context, kube.ObjectRef, time.Time) (bool, error) { retries++; return true, nil }

	result, requeue := f.deployDeferred(t, edgeprobe.StateUnreachable, foreignAddress)

	require.Equal(t, edgeProbeRolloutInterval, requeue, "a pending domain keeps the rollout pass on the probe cadence")
	require.NotNil(t, f.target(t).ActiveRevisionID, "a deferred certificate must not hold activation")
	require.Equal(t, result.RevisionID, *f.target(t).ActiveRevisionID)
	run, err := f.st.GetRunByID(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", run.Status)
	require.Zero(t, retries, "nothing to retry while the domain is elsewhere")

	step, found, err := f.kernel.deps.Journal.FindStep(ctx, result.RunID, "tls:"+certName)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "skipped", step.Status)
	message, err := f.kernel.deps.Journal.LatestStepMessage(ctx, step.ID)
	require.NoError(t, err)
	require.Contains(t, message, "TLS deferred · demo.example.com does not reach this installation yet")
	require.Contains(t, message, "edge state: unreachable")
	require.Contains(t, message, "203.0.113.9: answered by another server (HTTP 301 without Skali-Instance)")
	require.Contains(t, message, "Point the domain's A/AAAA records at this installation")
	require.NotContains(t, message, "deadline:", "a deferral has no deadline")

	status, err := f.kernel.Status(ctx, f.environmentID)
	require.NoError(t, err)
	require.Len(t, status.Services, 1)
	web := status.Services[0]
	require.Equal(t, module.HealthHealthy, web.Health)
	require.Len(t, web.Routes, 1)
	require.NotNil(t, web.Routes[0].Edge)
	require.Equal(t, "unreachable", web.Routes[0].Edge.State)
	require.Equal(t, "demo.example.com", web.Routes[0].Edge.Domain)
	require.Equal(t, []string{"203.0.113.9: answered by another server (HTTP 301 without Skali-Instance)"}, web.Routes[0].Edge.Addresses)
	require.NotNil(t, web.Routes[0].Certificate)
	require.Equal(t, "issuing", web.Routes[0].Certificate.State)
	var codes []string
	for _, diagnostic := range web.Diagnostics {
		codes = append(codes, diagnostic.Code)
	}
	require.Contains(t, codes, "certificate-deferred")
}

// One record moved and one left behind reads as partial, which counts as
// not arrived: the CA prefers IPv6 and would validate against the old host.
func TestCertificatePartialCountsAsDeferred(t *testing.T) {
	t.Parallel()
	f := certFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()

	result, _ := f.deployDeferred(t, edgeprobe.StatePartial, oursAddress,
		edgeprobe.AddressResult{Address: "2001:db8::9", Outcome: edgeprobe.OutcomeForeign, Detail: "HTTP 200 without Skali-Instance"})

	require.NotNil(t, f.target(t).ActiveRevisionID)
	step, _, err := f.kernel.deps.Journal.FindStep(ctx, result.RunID, "tls:"+certName)
	require.NoError(t, err)
	require.Equal(t, "skipped", step.Status)
	message, err := f.kernel.deps.Journal.LatestStepMessage(ctx, step.ID)
	require.NoError(t, err)
	require.Contains(t, message, "edge state: partial")
	require.Contains(t, message, "198.51.100.7: answered by this installation")
	require.Contains(t, message, "2001:db8::9: answered by another server")
}

// A probe that cannot say anything keeps today's gate: blocking until the
// certificate issues and failing at the deadline. A broken probe must never
// hide a real issuance failure.
func TestCertificateProbeUnknownKeepsGate(t *testing.T) {
	t.Parallel()
	f := certFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()

	result, requeue := f.deployDeferred(t, edgeprobe.StateUnknown)

	require.Equal(t, requeueHealthCheck, requeue)
	require.Nil(t, f.target(t).ActiveRevisionID, "an unknown verdict must not relax the gate")
	run, err := f.st.GetRunByID(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "running", run.Status)
	step, _, err := f.kernel.deps.Journal.FindStep(ctx, result.RunID, "tls:"+certName)
	require.NoError(t, err)
	require.Equal(t, "waiting", step.Status)

	f.kernel.cfg.RolloutDeadline = time.Nanosecond
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	run, err = f.st.GetRunByID(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "failed", run.Status)
}

// A domain that does reach this edge keeps the prompt failure on a backoff
// that outlives the budget: reachable means the failure is real.
func TestCertificateBackoffFailsPromptlyWhenReachable(t *testing.T) {
	f := certFixture(t, Config{RolloutDeadline: 10 * time.Minute})
	ctx := context.Background()
	f.kernel.deps.ProbeDomain = probeResult(nil, edgeprobe.StateReachable, oursAddress)
	result := f.executeDeploymentManifest(t, certManifest)
	f.fake.SetFresh()
	f.markHealthy(t)
	failure := f.target(t).UpdatedAt.Add(time.Second)
	f.fake.SetCertificate(f.environmentID, f.namespace, certName, "web", module.CertificateStatus{
		FailedAttempts: 2, LastFailureTime: failure, NextRetryTime: failure.Add(2 * time.Hour), Reason: "Failed", Message: "ACME order invalid", DNSNames: []string{"demo.example.com"},
	})
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	run, err := f.st.GetRunByID(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "failed", run.Status)
	step, _, err := f.kernel.deps.Journal.FindStep(ctx, result.RunID, "tls:"+certName)
	require.NoError(t, err)
	require.Equal(t, "failed", step.Status)
	status, err := f.kernel.Status(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, "reachable", status.Services[0].Routes[0].Edge.State)
}

// A converged environment with a deferred route keeps re-probing on the
// idle cadence without probing inside the interval.
func TestCertificateDeferredConvergedRequeues(t *testing.T) {
	t.Parallel()
	f := certFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	f.deployDeferred(t, edgeprobe.StateUnreachable, foreignAddress)
	var calls atomic.Int32
	f.kernel.deps.ProbeDomain = probeResult(&calls, edgeprobe.StateUnreachable, foreignAddress)

	requeue, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, edgeProbeIdleInterval, requeue)
	require.Zero(t, calls.Load(), "the cached verdict is fresh; no probe inside the interval")

	f.ageDomainProbe("demo.example.com")
	requeue, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, edgeProbeIdleInterval, requeue)
	require.EqualValues(t, 1, calls.Load())
}

// When the domain arrives, a certificate parked in cert-manager's failure
// backoff gets exactly one fresh issuance attempt, measured against now
// rather than the promotion time (the failure post-dates the promotion).
func TestCertificateArrivalTriggersRetryOnce(t *testing.T) {
	t.Parallel()
	f := certFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	f.deployDeferred(t, edgeprobe.StateUnreachable, foreignAddress)
	require.NotNil(t, f.target(t).ActiveRevisionID)

	now := time.Now()
	parked := module.CertificateStatus{FailedAttempts: 1, LastFailureTime: now.Add(-time.Minute),
		NextRetryTime: now.Add(32 * time.Hour), Reason: "Failed", Message: "ACME authorization failed"}
	f.fake.SetCertificate(f.environmentID, f.namespace, certName, "web", parked)
	retries := 0
	f.kernel.deps.RetryCertificate = func(_ context.Context, ref kube.ObjectRef, promoted time.Time) (bool, error) {
		require.Equal(t, certName, ref.Name)
		require.WithinDuration(t, time.Now(), promoted, 5*time.Second, "the arrival retry is measured against now")
		retries++
		issuing := parked
		issuing.Issuing = true
		issuing.NextRetryTime = time.Time{}
		f.fake.SetCertificate(f.environmentID, f.namespace, certName, "web", issuing)
		return true, nil
	}

	// Still elsewhere: nothing to retry.
	f.ageDomainProbe("demo.example.com")
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Zero(t, retries)

	// DNS moved: the next probe finds this edge and pushes issuance once.
	f.kernel.deps.ProbeDomain = probeResult(nil, edgeprobe.StateReachable, oursAddress)
	f.ageDomainProbe("demo.example.com")
	requeue, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, 1, retries)
	require.Equal(t, requeueHealthCheck, requeue,
		"a reachable domain needs no probe cadence; the issuing certificate gates health on the ordinary one")

	// Issuance is under way; the settled arrival does not retry again.
	f.ageDomainProbe("demo.example.com")
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, 1, retries)

	status, err := f.kernel.Status(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, "reachable", status.Services[0].Routes[0].Edge.State)
	for _, diagnostic := range status.Services[0].Diagnostics {
		require.NotEqual(t, "certificate-deferred", diagnostic.Code, "a reachable domain is no longer deferred")
	}
}

// issuedNamesStub answers a fixed list of issued names for the route
// Secret and counts its reads.
func issuedNamesStub(t *testing.T, f *kernelFixture, reads *atomic.Int32, names ...string) func(context.Context, string, string) ([]string, error) {
	return func(_ context.Context, namespace, secretName string) ([]string, error) {
		require.Equal(t, f.namespace, namespace)
		require.Equal(t, certName, secretName)
		if reads != nil {
			reads.Add(1)
		}
		return names, nil
	}
}

// activateUnderStagingDomain rolls revision A out under demo.example.com
// with an issued certificate; the migration tests rename the route from
// there.
func (f *kernelFixture) activateUnderStagingDomain(t *testing.T, notAfter time.Time) *deploy.ExecuteResult {
	t.Helper()
	ctx := context.Background()
	first := f.executeDeploymentManifest(t, certManifest)
	f.fake.SetFresh()
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	f.markHealthy(t)
	f.fake.SetCertificate(f.environmentID, f.namespace, certName, "web",
		module.CertificateStatus{Ready: true, NotAfter: notAfter, DNSNames: []string{"demo.example.com"}, SecretName: certName})
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.NotNil(t, f.target(t).ActiveRevisionID)
	require.Equal(t, first.RevisionID, *f.target(t).ActiveRevisionID)
	return first
}

// renameRoute promotes revision B with the production domain and lets
// cert-manager rename the Certificate in place: the old Secret keeps
// serving while a reissue for the new name is under way.
func (f *kernelFixture) renameRoute(t *testing.T, notAfter time.Time) *deploy.ExecuteResult {
	t.Helper()
	ctx := context.Background()
	second := f.executeDeploymentValues(t, certManifest, map[string]string{
		"APP_DOMAIN": "prod.example.com", "SESSION_SECRET": "kernel-plant-value",
	})
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	f.markHealthy(t)
	f.fake.SetCertificate(f.environmentID, f.namespace, certName, "web",
		module.CertificateStatus{Ready: false, Issuing: true, Reason: "RequestChanged",
			Message:  "Fields on existing CertificateRequest resource not up to date: [dnsNames]",
			NotAfter: notAfter, DNSNames: []string{"prod.example.com"}, SecretName: certName})
	return second
}

// newestReconcileRun is the reconcile-kind run created last.
func (f *kernelFixture) newestReconcileRun(t *testing.T) *store.Run {
	t.Helper()
	runs, err := f.journal.ListRuns(context.Background(), f.environmentID)
	require.NoError(t, err)
	var newest *store.Run
	for i := range runs {
		if runs[i].Kind == "reconcile" && (newest == nil || runs[i].CreatedAt.After(newest.CreatedAt)) {
			newest = &runs[i]
		}
	}
	return newest
}

func (f *kernelFixture) countRuns(t *testing.T) int {
	t.Helper()
	runs, err := f.journal.ListRuns(context.Background(), f.environmentID)
	require.NoError(t, err)
	return len(runs)
}

// The migration case: the route's domain changes on the same key while
// the new domain still points at the old host. The valid certificate on
// hand names the old domain, so for the new one it counts as unissued:
// the redeploy defers and activates, the old certificate keeps serving,
// and the arrival of the domain triggers one fresh issuance, recorded as
// a run, whose outcome closes in a later run.
func TestCertificateDomainChangeDefersAndArrives(t *testing.T) {
	t.Parallel()
	f := certFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	var reads atomic.Int32
	f.kernel.deps.IssuedNames = issuedNamesStub(t, f, &reads, "demo.example.com")
	f.kernel.deps.ProbeDomain = probeResult(nil, edgeprobe.StateReachable, oursAddress)
	notAfter := time.Now().Add(60 * 24 * time.Hour)
	f.activateUnderStagingDomain(t, notAfter)
	require.EqualValues(t, 1, reads.Load(), "the issued names are read once per issuance")

	var probes atomic.Int32
	f.kernel.deps.ProbeDomain = func(ctx context.Context, domain string) edgeprobe.Result {
		require.Equal(t, "prod.example.com", domain, "only the desired domain is probed")
		return probeResult(&probes, edgeprobe.StateUnreachable, foreignAddress)(ctx, domain)
	}
	second := f.renameRoute(t, notAfter)
	requeue, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.LessOrEqual(t, requeue, edgeProbeRolloutInterval, "a pending domain keeps the pass on the probe cadence at most")
	require.Equal(t, second.RevisionID, *f.target(t).ActiveRevisionID, "a deferred rename must not hold activation")
	run, err := f.st.GetRunByID(ctx, second.RunID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", run.Status)
	require.EqualValues(t, 1, reads.Load(), "the same notAfter needs no second read")
	require.EqualValues(t, 1, probes.Load())

	step, found, err := f.journal.FindStep(ctx, second.RunID, "tls:"+certName)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "skipped", step.Status)
	message, err := f.journal.LatestStepMessage(ctx, step.ID)
	require.NoError(t, err)
	require.Contains(t, message, "TLS deferred · prod.example.com does not reach this installation yet · the certificate for demo.example.com keeps serving")
	require.Contains(t, message, "issued for: demo.example.com")

	status, err := f.kernel.Status(ctx, f.environmentID)
	require.NoError(t, err)
	web := status.Services[0]
	require.Equal(t, module.HealthHealthy, web.Health)
	require.NotNil(t, web.Routes[0].Edge)
	require.True(t, web.Routes[0].Edge.Deferred)
	require.Equal(t, "unreachable", web.Routes[0].Edge.State)
	require.Equal(t, "prod.example.com", web.Routes[0].Edge.Domain)
	var codes []string
	for _, diagnostic := range web.Diagnostics {
		codes = append(codes, diagnostic.Code)
		if diagnostic.Code == "certificate-deferred" {
			require.Contains(t, diagnostic.Message, "the certificate for demo.example.com keeps serving")
		}
	}
	require.Contains(t, codes, "certificate-deferred")
	require.NotContains(t, codes, "certificate-renewal-failing")

	// The reissue fails while DNS is elsewhere; then the domain arrives:
	// one retry measured against now, and a run records the arrival.
	now := time.Now()
	f.fake.SetCertificate(f.environmentID, f.namespace, certName, "web",
		module.CertificateStatus{Ready: false, NotAfter: notAfter, DNSNames: []string{"prod.example.com"}, SecretName: certName,
			FailedAttempts: 1, LastFailureTime: now.Add(-time.Minute), NextRetryTime: now.Add(time.Hour),
			Reason: "Failed", Message: "ACME authorization failed"})
	retries := 0
	f.kernel.deps.RetryCertificate = func(_ context.Context, ref kube.ObjectRef, promoted time.Time) (bool, error) {
		require.Equal(t, certName, ref.Name)
		require.WithinDuration(t, time.Now(), promoted, 5*time.Second)
		retries++
		return true, nil
	}
	runsBefore := f.countRuns(t)
	f.kernel.deps.ProbeDomain = probeResult(nil, edgeprobe.StateReachable, oursAddress)
	f.ageDomainProbe("prod.example.com")
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, 1, retries)
	require.Equal(t, runsBefore+1, f.countRuns(t), "the arrival is one run")
	arrivalRun := f.newestReconcileRun(t)
	require.NotNil(t, arrivalRun)
	require.Equal(t, "succeeded", arrivalRun.Status)
	step, found, err = f.journal.FindStep(ctx, arrivalRun.ID, "arrival:"+certName)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "succeeded", step.Status)
	message, err = f.journal.LatestStepMessage(ctx, step.ID)
	require.NoError(t, err)
	require.Contains(t, message, "A fresh issuance was requested · attempt 2")

	// A pass that changes nothing records nothing.
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, runsBefore+1, f.countRuns(t))

	// The certificate for the new name lands: the outcome run closes the story.
	f.kernel.deps.IssuedNames = issuedNamesStub(t, f, &reads, "prod.example.com")
	f.fake.SetCertificate(f.environmentID, f.namespace, certName, "web",
		module.CertificateStatus{Ready: true, NotAfter: time.Now().Add(90 * 24 * time.Hour), DNSNames: []string{"prod.example.com"}, SecretName: certName})
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, runsBefore+2, f.countRuns(t), "the outcome is one more run")
	outcomeRun := f.newestReconcileRun(t)
	require.Equal(t, "succeeded", outcomeRun.Status)
	step, found, err = f.journal.FindStep(ctx, outcomeRun.ID, "tls:"+certName)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "succeeded", step.Status)
	message, err = f.journal.LatestStepMessage(ctx, step.ID)
	require.NoError(t, err)
	require.Contains(t, message, "TLS certificate issued for prod.example.com")
	require.EqualValues(t, 2, reads.Load(), "a new notAfter reads the names again")

	status, err = f.kernel.Status(ctx, f.environmentID)
	require.NoError(t, err)
	require.False(t, status.Services[0].Routes[0].Edge.Deferred)
	for _, diagnostic := range status.Services[0].Diagnostics {
		require.NotEqual(t, "certificate-deferred", diagnostic.Code)
	}
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, runsBefore+2, f.countRuns(t), "a settled route records nothing more")
}

// An issuance that fails after the domain arrived closes the story with a
// failed run naming cert-manager's reason.
func TestCertificateArrivalOutcomeFails(t *testing.T) {
	t.Parallel()
	f := certFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	f.deployDeferred(t, edgeprobe.StateUnreachable, foreignAddress)
	now := time.Now()
	f.fake.SetCertificate(f.environmentID, f.namespace, certName, "web",
		module.CertificateStatus{FailedAttempts: 1, LastFailureTime: now.Add(-time.Minute),
			NextRetryTime: now.Add(time.Hour), Reason: "Failed", Message: "ACME authorization failed", SecretName: certName})
	f.kernel.deps.RetryCertificate = func(context.Context, kube.ObjectRef, time.Time) (bool, error) { return true, nil }
	f.kernel.deps.ProbeDomain = probeResult(nil, edgeprobe.StateReachable, oursAddress)
	f.ageDomainProbe("demo.example.com")
	runsBefore := f.countRuns(t)
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, runsBefore+1, f.countRuns(t))

	// The attempt after the arrival fails too.
	f.fake.SetCertificate(f.environmentID, f.namespace, certName, "web",
		module.CertificateStatus{FailedAttempts: 2, LastFailureTime: time.Now().Add(2 * time.Second),
			NextRetryTime: time.Now().Add(2 * time.Hour), Reason: "Failed", Message: "urn:ietf:params:acme:error:rateLimited", SecretName: certName})
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, runsBefore+2, f.countRuns(t))
	outcome := f.newestReconcileRun(t)
	require.Equal(t, "failed", outcome.Status)
	step, found, err := f.journal.FindStep(ctx, outcome.ID, "tls:"+certName)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "failed", step.Status)
	message, err := f.journal.LatestStepMessage(ctx, step.ID)
	require.NoError(t, err)
	require.Contains(t, message, "TLS issuance failed after demo.example.com arrived · attempt 2: urn:ietf:params:acme:error:rateLimited")

	// The failure is told once.
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, runsBefore+2, f.countRuns(t))
}

// A renamed route whose new domain already reaches this edge gates like a
// first issuance: activation waits on the certificate for the new name,
// and the deadline fails the run and falls back to the staging revision.
func TestCertificateDomainChangeReachableGates(t *testing.T) {
	t.Parallel()
	f := certFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	f.kernel.deps.IssuedNames = issuedNamesStub(t, f, nil, "demo.example.com")
	f.kernel.deps.ProbeDomain = probeResult(nil, edgeprobe.StateReachable, oursAddress)
	notAfter := time.Now().Add(60 * 24 * time.Hour)
	first := f.activateUnderStagingDomain(t, notAfter)

	second := f.renameRoute(t, notAfter)
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, first.RevisionID, *f.target(t).ActiveRevisionID, "a valid certificate for the old name must not activate the new one")
	step, found, err := f.journal.FindStep(ctx, second.RunID, "tls:"+certName)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "waiting", step.Status)
	message, err := f.journal.LatestStepMessage(ctx, step.ID)
	require.NoError(t, err)
	require.Contains(t, message, "Issuing TLS certificate · attempt 1 · the certificate for demo.example.com keeps serving")

	status, err := f.kernel.Status(ctx, f.environmentID)
	require.NoError(t, err)
	web := status.Services[0]
	require.Equal(t, module.HealthProgressing, web.Health)
	require.NotNil(t, web.Routes[0].Edge)
	require.False(t, web.Routes[0].Edge.Deferred)
	require.Equal(t, "certificate-pending", web.Diagnostics[0].Code)
	require.Contains(t, web.Diagnostics[0].Message, "is not issued for prod.example.com yet (issued for demo.example.com)")

	f.kernel.cfg.RolloutDeadline = time.Nanosecond
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	run, err := f.st.GetRunByID(ctx, second.RunID)
	require.NoError(t, err)
	require.Equal(t, "failed", run.Status)
	require.Equal(t, first.RevisionID, *f.target(t).TargetRevisionID, "the target falls back to the staging revision")
}

// A valid certificate that covers its domain is never probed: the edge
// verdict only matters for a route without a usable certificate.
func TestCertificateCoveredSkipsProbe(t *testing.T) {
	t.Parallel()
	f := certFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	var reads, probes atomic.Int32
	f.kernel.deps.IssuedNames = issuedNamesStub(t, f, &reads, "demo.example.com")
	// The pass before the Certificate is observed probes once; from the
	// issuance on, the verdict is irrelevant however the probe answers.
	f.kernel.deps.ProbeDomain = probeResult(nil, edgeprobe.StateReachable, oursAddress)
	f.activateUnderStagingDomain(t, time.Now().Add(60*24*time.Hour))
	f.kernel.deps.ProbeDomain = probeResult(&probes, edgeprobe.StateUnreachable, foreignAddress)
	f.ageDomainProbe("demo.example.com")
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Zero(t, probes.Load())
	require.EqualValues(t, 1, reads.Load())
	status, err := f.kernel.Status(ctx, f.environmentID)
	require.NoError(t, err)
	require.False(t, status.Services[0].Routes[0].Edge.Deferred)
}

// The Certificate name hashes project, application and route only, so two
// environments of one project share it; their verdicts stay apart.
func TestEdgeStatusKeyedByEnvironment(t *testing.T) {
	t.Parallel()
	k := New(Deps{}, Config{})
	now := time.Now()
	staging, production := uuid.New(), uuid.New()
	k.recordProbe("staging.example.com", edgeprobe.Result{Domain: "staging.example.com", State: edgeprobe.StateReachable}, now, false)
	k.recordProbe("prod.example.com", edgeprobe.Result{Domain: "prod.example.com", State: edgeprobe.StateUnreachable}, now, false)
	k.noteRoute(routeKeyOf(staging, certName), "staging.example.com", true, false, false)
	k.noteRoute(routeKeyOf(production, certName), "prod.example.com", false, true, true)

	require.Equal(t, "reachable", k.edgeStatus(staging, certName).State)
	require.False(t, k.edgeStatus(staging, certName).Deferred)
	require.Equal(t, "unreachable", k.edgeStatus(production, certName).State)
	require.True(t, k.edgeStatus(production, certName).Deferred)
	require.Nil(t, k.edgeStatus(uuid.New(), certName))
}

// A manual probe replaces the cached verdicts at once, enqueues the
// environment, and counts a reachable domain as arrived for a route
// without a usable certificate, so the next pass requests one fresh
// issuance even though the domain was already here.
func TestProbeRoutesForcesArrival(t *testing.T) {
	t.Parallel()
	f := certFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	f.deployDeferred(t, edgeprobe.StateUnreachable, foreignAddress)
	now := time.Now()
	f.fake.SetCertificate(f.environmentID, f.namespace, certName, "web",
		module.CertificateStatus{FailedAttempts: 1, LastFailureTime: now.Add(-time.Minute),
			NextRetryTime: now.Add(time.Hour), Reason: "Failed", Message: "ACME authorization failed", SecretName: certName})
	retries := 0
	f.kernel.deps.RetryCertificate = func(context.Context, kube.ObjectRef, time.Time) (bool, error) { retries++; return true, nil }

	// The domain arrives and the pass settles the arrival with one retry.
	f.kernel.deps.ProbeDomain = probeResult(nil, edgeprobe.StateReachable, oursAddress)
	f.ageDomainProbe("demo.example.com")
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, 1, retries)

	// The attempt fails again; the reconciler would now wait out the
	// backoff. A manual probe of the still-reachable domain forces a
	// second arrival.
	f.fake.SetCertificate(f.environmentID, f.namespace, certName, "web",
		module.CertificateStatus{FailedAttempts: 2, LastFailureTime: time.Now().Add(2 * time.Second),
			NextRetryTime: time.Now().Add(2 * time.Hour), Reason: "Failed", Message: "rate limited", SecretName: certName})
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, 1, retries, "a reachable domain that stayed reachable is no new arrival")

	var calls atomic.Int32
	f.kernel.deps.ProbeDomain = probeResult(&calls, edgeprobe.StateReachable, oursAddress)
	probes, err := f.kernel.ProbeRoutes(ctx, f.environmentID)
	require.NoError(t, err)
	require.Len(t, probes, 1)
	require.Equal(t, "web", probes[0].Service)
	require.Equal(t, "public", probes[0].Key)
	require.Equal(t, "demo.example.com", probes[0].Domain)
	require.Equal(t, edgeprobe.StateReachable, probes[0].Result.State)
	require.Equal(t, []edgeprobe.AddressResult{oursAddress}, probes[0].Result.Addresses)
	require.EqualValues(t, 1, calls.Load(), "a manual probe ignores the cache interval")

	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, 2, retries, "the forced arrival requests one more issuance")
	require.Equal(t, "succeeded", f.newestReconcileRun(t).Status)

	// A route with a usable certificate is probed but never re-arrives.
	f.fake.SetCertificate(f.environmentID, f.namespace, certName, "web",
		module.CertificateStatus{Ready: true, NotAfter: time.Now().Add(60 * 24 * time.Hour), DNSNames: []string{"demo.example.com"}, SecretName: certName})
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	_, err = f.kernel.ProbeRoutes(ctx, f.environmentID)
	require.NoError(t, err)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, 2, retries)
}

// Without a prober (no cert-manager) the manual probe says so instead of
// answering nothing.
func TestProbeRoutesWithoutProber(t *testing.T) {
	t.Parallel()
	f := certFixture(t, Config{RolloutDeadline: time.Hour})
	f.executeDeploymentManifest(t, certManifest)
	_, err := f.kernel.ProbeRoutes(context.Background(), f.environmentID)
	require.ErrorIs(t, err, ErrNoEdgeProbe)
}
