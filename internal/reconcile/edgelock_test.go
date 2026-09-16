package reconcile

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/edge/edgeprobe"
	"github.com/Hinkolas/skali/internal/journal"
)

// signallingProbe answers like probeResult and closes entered on the first
// call, after sleeping for delay.
func signallingProbe(entered chan struct{}, delay time.Duration, state edgeprobe.State, addresses ...edgeprobe.AddressResult) func(context.Context, string) edgeprobe.Result {
	var once sync.Once
	stub := probeResult(nil, state, addresses...)
	return func(ctx context.Context, domain string) edgeprobe.Result {
		once.Do(func() { close(entered) })
		time.Sleep(delay)
		return stub(ctx, domain)
	}
}

// The edge probe runs before the pass takes the environment lock: a pass
// whose lock is held by someone else (a Promote, in production) still
// probes, and only then waits.
func TestReconcileProbesBeforeEnvironmentLock(t *testing.T) {
	t.Parallel()
	f := certFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	f.deployDeferred(t, edgeprobe.StateUnreachable, foreignAddress)
	f.ageDomainProbe("demo.example.com")

	unlock, err := f.st.LockEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	probed := make(chan struct{})
	f.kernel.deps.ProbeDomain = signallingProbe(probed, 0, edgeprobe.StateUnreachable, foreignAddress)

	done := make(chan struct{})
	var requeue time.Duration
	var passErr error
	go func() {
		defer close(done)
		requeue, passErr = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	}()
	select {
	case <-probed:
	case <-time.After(5 * time.Second):
		t.Fatal("the pass did not probe while the environment lock was held elsewhere")
	}
	select {
	case <-done:
		t.Fatal("the pass finished without the environment lock")
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	<-done
	require.NoError(t, passErr)
	require.Equal(t, edgeProbeIdleInterval, requeue)
}

// While a probe is in flight the environment lock is free: another party
// takes it without waiting for the network.
func TestReconcileLockHoldExcludesProbeDuration(t *testing.T) {
	t.Parallel()
	f := certFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	f.deployDeferred(t, edgeprobe.StateUnreachable, foreignAddress)
	f.ageDomainProbe("demo.example.com")

	entered := make(chan struct{})
	f.kernel.deps.ProbeDomain = signallingProbe(entered, 500*time.Millisecond, edgeprobe.StateUnreachable, foreignAddress)
	done := make(chan error, 1)
	go func() {
		_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
		done <- err
	}()
	<-entered
	started := time.Now()
	unlock, err := f.st.LockEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Less(t, time.Since(started), 300*time.Millisecond, "the lock waited for the probe")
	unlock()
	require.NoError(t, <-done)
}

// A pass that finds no verdict for a route domain (here: the last pass
// proved a usable certificate for it, so the pre-lock phase skipped the
// probe, and the certificate has since stopped being usable) judges the
// route unknown, probes nothing under the lock, and requeues quickly; the
// next pass probes before it locks.
func TestCertificateUnknownVerdictRequeuesQuickly(t *testing.T) {
	t.Parallel()
	f := certFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	f.deployDeferred(t, edgeprobe.StateUnreachable, foreignAddress)

	f.kernel.domainMu.Lock()
	delete(f.kernel.domains, "demo.example.com")
	key := routeKeyOf(f.environmentID, certName)
	record := f.kernel.routes[key]
	record.usable, record.domain = true, "demo.example.com"
	f.kernel.routes[key] = record
	f.kernel.domainMu.Unlock()

	var calls atomic.Int32
	f.kernel.deps.ProbeDomain = probeResult(&calls, edgeprobe.StateUnreachable, foreignAddress)
	requeue, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Zero(t, calls.Load(), "no probe under the lock")
	require.Equal(t, edgeProbeMissRequeue, requeue)
	status, err := f.kernel.Status(ctx, f.environmentID)
	require.NoError(t, err)
	require.Nil(t, status.Services[0].Routes[0].Edge, "no verdict to show yet")

	requeue, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.EqualValues(t, 1, calls.Load(), "the next pre-lock phase probes")
	require.Equal(t, edgeProbeIdleInterval, requeue)
	status, err = f.kernel.Status(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, "unreachable", status.Services[0].Routes[0].Edge.State)
}

// The pre-lock phase skips only routes whose last pass proved a usable
// certificate for the very domain they want now, and never re-probes a
// fresh verdict.
func TestProbeDueSkipsUsableSameDomainOnly(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	k := New(Deps{ProbeDomain: probeResult(&calls, edgeprobe.StateReachable, oursAddress)}, Config{})
	environmentID := uuid.New()
	k.noteRoute(routeKeyOf(environmentID, "cert-a"), "a.example.com", true, false, false)
	now := time.Now()

	k.probeDue(context.Background(), environmentID, []RouteProbe{{Domain: "a.example.com", certificate: "cert-a"}}, now, edgeProbeIdleInterval)
	require.Zero(t, calls.Load(), "usable for the same domain: the pass will not ask")

	k.probeDue(context.Background(), environmentID, []RouteProbe{{Domain: "b.example.com", certificate: "cert-a"}}, now, edgeProbeIdleInterval)
	require.EqualValues(t, 1, calls.Load(), "the route wants another domain")

	k.probeDue(context.Background(), environmentID, []RouteProbe{{Domain: "b.example.com", certificate: "cert-a"}}, now, edgeProbeIdleInterval)
	require.EqualValues(t, 1, calls.Load(), "fresh verdicts are not re-probed")

	_, _, known, fresh := k.lookupDomain("b.example.com", now, edgeProbeIdleInterval)
	require.True(t, known)
	require.True(t, fresh)
	_, _, known, _ = k.lookupDomain("a.example.com", now, edgeProbeIdleInterval)
	require.False(t, known, "a skipped domain gains no entry")
}

// The read-only adoption decision agrees with attachRun: runs the backup
// controller owns are never adopted, a deployment run past its artifact
// window is.
func TestAdoptableRunMatchesAttachRun(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{})
	ctx := context.Background()

	backup, err := f.journal.CreateRun(ctx, journal.RunInput{
		Kind: "backup", ProjectID: f.projectID, EnvironmentID: f.environmentID, Actor: "tester",
	})
	require.NoError(t, err)
	require.NoError(t, f.journal.StartRun(ctx, backup.ID))
	_, ok := f.kernel.adoptableRun(ctx, f.environmentID)
	require.False(t, ok, "a backup run belongs to the backup controller")
	require.NoError(t, f.journal.FinishRun(ctx, backup.ID, journal.RunSucceeded))

	_, ok = f.kernel.adoptableRun(ctx, f.environmentID)
	require.False(t, ok, "nothing running")

	result := f.executeDeploymentManifest(t, kernelManifest)
	run, ok := f.kernel.adoptableRun(ctx, f.environmentID)
	require.True(t, ok)
	require.Equal(t, result.RunID, run.ID)
	require.Equal(t, "deployment", run.Kind)
}
