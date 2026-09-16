package reconcile

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/deploy"
	"github.com/Hinkolas/skali/internal/edge/edgeprobe"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/module/app"
	"github.com/Hinkolas/skali/internal/module/bucket"
	"github.com/Hinkolas/skali/internal/module/database"
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
