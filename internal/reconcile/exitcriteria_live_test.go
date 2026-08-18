// The reconciliation exit-criteria suite. Each test names one
// criterion and runs against the live k3d cluster (task k3d:up), gated on
// TEST_KUBECONFIG plus TEST_DATABASE_URL.
package reconcile

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubetest"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/module/app"
	"github.com/Hinkolas/skali/internal/observe"
)

func (f *liveFixture) status(t *testing.T) *Status {
	t.Helper()
	status, err := f.kernel.Status(context.Background(), f.environmentID)
	require.NoError(t, err)
	return status
}

func (f *liveFixture) readyPods(t *testing.T) int {
	t.Helper()
	status := f.status(t)
	if len(status.Services) == 0 {
		return 0
	}
	ready := 0
	for _, pod := range status.Services[0].Pods {
		if pod.Ready {
			ready++
		}
	}
	return ready
}

// Exit criterion 1: pod create/readiness/delete changes propagate without
// waiting for the audit interval. Audit and resync are an hour away, so
// only watches can explain what this test observes. The two-second
// objective is measured and logged, not asserted (section 7.5).
func TestLivePodEventPropagation(t *testing.T) {
	t.Parallel()
	f := newLiveFixture(t, Config{RolloutDeadline: 5 * time.Minute, Audit: time.Hour}, nil)
	f.start(t)
	ctx := context.Background()

	result := f.deployManifest(t, liveManifest(f.projectName, 2, false))
	f.waitActive(t, result.RevisionID, 3*time.Minute)
	require.Eventually(t, func() bool { return f.readyPods(t) == 2 },
		time.Minute, 200*time.Millisecond)

	pods, err := f.clientset.CoreV1().Pods(f.namespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, pods.Items)

	deleted := time.Now()
	require.NoError(t, f.clientset.CoreV1().Pods(f.namespace).Delete(ctx, pods.Items[0].Name, metav1.DeleteOptions{}))

	require.Eventually(t, func() bool { return f.readyPods(t) < 2 },
		10*time.Second, 50*time.Millisecond,
		"the pod deletion must reach the status projection without the audit")
	t.Logf("pod deletion visible after %s (objective: 2s)", time.Since(deleted).Round(time.Millisecond))

	recovered := time.Now()
	require.Eventually(t, func() bool { return f.readyPods(t) == 2 },
		time.Minute, 200*time.Millisecond,
		"the replacement pod's readiness must propagate")
	t.Logf("replacement readiness visible after %s", time.Since(recovered).Round(time.Millisecond))
}

// Exit criterion 3: deleting a managed stateless object out of band causes
// healing, and healing works identically with the journal wiped first
// (runs explain, never drive).
func TestLiveOutOfBandDeletionHeals(t *testing.T) {
	t.Parallel()
	f := newLiveFixture(t, Config{RolloutDeadline: 5 * time.Minute}, nil)
	f.start(t)
	ctx := context.Background()

	result := f.deployManifest(t, liveManifest(f.projectName, 1, false))
	f.waitActive(t, result.RevisionID, 3*time.Minute)
	deploymentName := f.projectName + "-web"

	heal := func(t *testing.T) {
		t.Helper()
		before, err := f.clientset.AppsV1().Deployments(f.namespace).Get(ctx, deploymentName, metav1.GetOptions{})
		require.NoError(t, err)
		require.NoError(t, f.clientset.AppsV1().Deployments(f.namespace).Delete(ctx, deploymentName, metav1.DeleteOptions{}))
		require.Eventually(t, func() bool {
			live, err := f.clientset.AppsV1().Deployments(f.namespace).Get(ctx, deploymentName, metav1.GetOptions{})
			return err == nil && live.UID != before.UID
		}, time.Minute, 200*time.Millisecond, "the watch-driven enqueue must recreate the deployment")
		require.Eventually(t, func() bool {
			status := f.status(t)
			return len(status.Services) == 1 && status.Services[0].Health == module.HealthHealthy
		}, 2*time.Minute, 200*time.Millisecond, "health must return after healing")
	}

	heal(t)
	runs, err := f.journal.ListRuns(ctx, f.environmentID)
	require.NoError(t, err)
	foundHeal := false
	for _, run := range runs {
		if run.Kind == "reconcile" {
			foundHeal = true
		}
	}
	require.True(t, foundHeal, "a reconcile run documents the heal")

	// The same heal with zero journal rows: behavior is identical.
	_, err = f.st.Pool.Exec(ctx, "DELETE FROM runs")
	require.NoError(t, err)
	heal(t)

	target, err := f.st.GetEnvironmentTarget(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, result.RevisionID, *target.ActiveRevisionID)
}

// Exit criterion 4: reapplying an autoscaled application neither conflicts
// with the HPA nor resets its current replica count. Covers both scale-mode
// transitions; a watch tap proves no transient reset to the default.
func TestLiveHPAOwnershipAndTransitions(t *testing.T) {
	t.Parallel()
	f := newLiveFixture(t, Config{RolloutDeadline: 5 * time.Minute}, nil)
	f.start(t)
	ctx := context.Background()
	deploymentName := f.projectName + "-web"

	// A tap recording every replica count the API server ever serves for
	// the deployment; the default-reset bug would surface as a 1 here.
	tapCtx, stopTap := context.WithCancel(ctx)
	defer stopTap()
	seen := make(chan int32, 256)
	go func() {
		watcher, err := f.clientset.AppsV1().Deployments(f.namespace).Watch(tapCtx, metav1.ListOptions{
			FieldSelector: "metadata.name=" + deploymentName,
		})
		if err != nil {
			return
		}
		defer watcher.Stop()
		for event := range watcher.ResultChan() {
			if deployment, ok := event.Object.(*appsv1.Deployment); ok && deployment.Spec.Replicas != nil {
				select {
				case seen <- *deployment.Spec.Replicas:
				default:
				}
			}
		}
	}()

	// Phase a: fixed 3 replicas, then transition to autoscaled.
	fixed := f.deployManifest(t, liveManifest(f.projectName, 3, false))
	f.waitActive(t, fixed.RevisionID, 3*time.Minute)

	autoscaled := f.deployManifest(t, liveManifest(f.projectName, 1, true))
	f.waitActive(t, autoscaled.RevisionID, 3*time.Minute)

	live, err := f.clientset.AppsV1().Deployments(f.namespace).Get(ctx, deploymentName, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, live.Spec.Replicas)
	require.Equal(t, int32(3), *live.Spec.Replicas,
		"releasing replicas ownership must not move the live count")
	require.False(t, kube.OwnsField(live.ManagedFields, kube.FieldManagerProject, kube.ReplicasFieldPath),
		"skalid must no longer own spec.replicas while the HPA is active")

	// Phase b: a foreign controller scales; reapplying the unchanged
	// revision neither conflicts nor moves the count back.
	scale, err := f.clientset.AppsV1().Deployments(f.namespace).GetScale(ctx, deploymentName, metav1.GetOptions{})
	require.NoError(t, err)
	scale.Spec.Replicas = 4
	_, err = f.clientset.AppsV1().Deployments(f.namespace).UpdateScale(ctx, deploymentName, scale,
		metav1.UpdateOptions{FieldManager: "test-hpa-controller"})
	require.NoError(t, err)

	f.kernel.Enqueue(f.environmentID)
	require.Never(t, func() bool {
		live, err := f.clientset.AppsV1().Deployments(f.namespace).Get(ctx, deploymentName, metav1.GetOptions{})
		return err != nil || live.Spec.Replicas == nil || *live.Spec.Replicas != 4
	}, 5*time.Second, 250*time.Millisecond,
		"reapplying the autoscaled revision must not move the externally scaled count")

	// Phase c: back to fixed 2; the HPA goes away and skalid retakes the
	// field with an explicit value.
	fixedAgain := f.deployManifest(t, liveManifest(f.projectName, 2, false))
	f.waitActive(t, fixedAgain.RevisionID, 3*time.Minute)
	require.Eventually(t, func() bool {
		_, err := f.clientset.AutoscalingV2().HorizontalPodAutoscalers(f.namespace).Get(ctx, deploymentName, metav1.GetOptions{})
		return err != nil
	}, time.Minute, 200*time.Millisecond, "the autoscaler must be pruned")
	live, err = f.clientset.AppsV1().Deployments(f.namespace).Get(ctx, deploymentName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, int32(2), *live.Spec.Replicas)
	require.True(t, kube.OwnsField(live.ManagedFields, kube.FieldManagerProject, kube.ReplicasFieldPath),
		"skalid owns spec.replicas again in fixed mode")

	stopTap()
	close(seen)
	for count := range seen {
		require.NotEqual(t, int32(1), count,
			"no transition may transiently reset replicas to the default")
	}
}

// Exit criterion 5: API topology reads perform no direct Kubernetes
// request. A counting transport wraps every kernel connection; repeated
// status reads must not move the non-watch request count.
func TestStatusReadsPerformNoKubernetesRequests(t *testing.T) {
	t.Parallel()
	config := kubetest.Config(t)
	counter := kubetest.CountNonWatch(config)
	f := newLiveFixture(t, Config{RolloutDeadline: 5 * time.Minute}, config)
	f.start(t)

	result := f.deployManifest(t, liveManifest(f.projectName, 1, false))
	f.waitActive(t, result.RevisionID, 3*time.Minute)

	// Let in-flight reconcile passes drain, then snapshot the counter.
	var before int64
	require.Eventually(t, func() bool {
		current := counter.Load()
		time.Sleep(2 * time.Second)
		if counter.Load() == current {
			before = current
			return true
		}
		return false
	}, time.Minute, 100*time.Millisecond, "background activity must settle")

	for range 25 {
		status := f.status(t)
		require.NotNil(t, status.Active)
		require.Len(t, status.Services, 1)
		require.Equal(t, module.HealthHealthy, status.Services[0].Health)
	}
	require.Equal(t, before, counter.Load(),
		"status reads must be served from the observed store, never the cluster")
}

// Exit criterion 6: restart rebuilds the cache before reporting fresh
// health. A second kernel over the same database and cluster reports
// unknown until its own sync completes, then fresh health, without
// spuriously re-applying the unchanged revision.
func TestLiveRestartRebuildsCacheBeforeFreshHealth(t *testing.T) {
	t.Parallel()
	f := newLiveFixture(t, Config{RolloutDeadline: 5 * time.Minute}, nil)
	stop := f.start(t)
	ctx := context.Background()

	result := f.deployManifest(t, liveManifest(f.projectName, 1, false))
	f.waitActive(t, result.RevisionID, 3*time.Minute)
	deploymentName := f.projectName + "-web"
	before, err := f.clientset.AppsV1().Deployments(f.namespace).Get(ctx, deploymentName, metav1.GetOptions{})
	require.NoError(t, err)

	stop()

	// A new boot: fresh executor identity, recovery, its own observation.
	restarted := restartKernel(t, f)
	status, err := restarted.kernel.Status(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, module.SourceUnknown, status.Observation.State,
		"before the cache rebuild the observation is unknown")
	require.Len(t, status.Services, 1)
	require.Equal(t, module.HealthUnknown, status.Services[0].Health,
		"health must be unknown before the cache rebuild, never guessed")

	restarted.start(t)
	require.Eventually(t, func() bool {
		status, err := restarted.kernel.Status(ctx, f.environmentID)
		return err == nil && status.Observation.State == module.SourceFresh &&
			len(status.Services) == 1 && status.Services[0].Health == module.HealthHealthy
	}, time.Minute, 200*time.Millisecond, "fresh health only after the rebuild")

	// The unchanged revision must be a server-side-apply no-op: the
	// deployment's generation did not move across the restart.
	after, err := f.clientset.AppsV1().Deployments(f.namespace).Get(ctx, deploymentName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, before.Generation, after.Generation)

	target, err := f.st.GetEnvironmentTarget(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, result.RevisionID, *target.ActiveRevisionID)
}

// Section 17.3 (destructive): stop and restart the whole cluster while
// retaining state, then prove observation and reconciliation recover. Run
// alone (TEST_K3D_DESTRUCTIVE=1): restarting the API server disrupts every
// other live test on the shared cluster.
func TestLiveClusterRestartRecovery(t *testing.T) {
	if os.Getenv("TEST_K3D_DESTRUCTIVE") == "" {
		t.Skip("set TEST_K3D_DESTRUCTIVE=1 to run the cluster-restart test (disrupts other live tests; run alone)")
	}
	f := newLiveFixture(t, Config{RolloutDeadline: 10 * time.Minute, StaleThreshold: 5 * time.Second}, nil)
	f.start(t)
	ctx := context.Background()

	result := f.deployManifest(t, liveManifest(f.projectName, 1, false))
	f.waitActive(t, result.RevisionID, 3*time.Minute)

	// While the cluster is down, the outage must surface as stale; a k3s
	// restart is fast enough that asserting after the start would race the
	// recovery.
	clusterContainer(t, "stop")
	require.Eventually(t, func() bool {
		state := f.observed.Source().State
		return state == module.SourceStale || state == module.SourceUnknown
	}, 2*time.Minute, 500*time.Millisecond, "the outage must surface as stale or unknown")

	clusterContainer(t, "start")
	require.Eventually(t, func() bool {
		status, err := f.kernel.Status(ctx, f.environmentID)
		return err == nil && status.Observation.State == module.SourceFresh &&
			len(status.Services) == 1 && status.Services[0].Health == module.HealthHealthy
	}, 10*time.Minute, time.Second, "observation and reconciliation must recover after the restart")
}

// restartKernel builds a second boot over the fixture's database and
// cluster: new executor id, journal recovery, fresh observation.
func restartKernel(t *testing.T, f *liveFixture) *liveFixture {
	t.Helper()
	restarted := *f
	client, err := kube.NewFromConfig(kubetest.Config(t))
	require.NoError(t, err)
	journalSvc := journal.NewService(f.st, "live-boot-restarted")
	if _, err := journalSvc.RecoverOnBoot(context.Background()); err != nil {
		t.Fatalf("recover on boot: %v", err)
	}
	observed := observe.NewStore(nil)
	var kernel *Kernel
	source := observe.NewKubeSource(client, observed, observe.SourceOptions{
		Resync:         time.Hour,
		StaleThreshold: 30 * time.Second,
		Enqueue:        func(id uuid.UUID) { kernel.Enqueue(id) },
	})
	registry := module.NewRegistry()
	require.NoError(t, registry.Register(app.Module{}))
	kernel = New(Deps{
		Store: f.st, Deploy: f.deploy, Values: f.values, Journal: journalSvc,
		Registry: registry, Observed: observed, Source: source, Cluster: client,
	}, Config{RolloutDeadline: 5 * time.Minute})
	restarted.kernel = kernel
	restarted.observed = observed
	restarted.journal = journalSvc
	return &restarted
}

func clusterContainer(t *testing.T, action string) {
	t.Helper()
	if output, err := exec.Command("docker", action, "skali-test").CombinedOutput(); err != nil {
		t.Fatalf("docker %s: %v: %s", action, err, output)
	}
	if action == "stop" {
		// If the test dies mid-outage, leave the shared cluster running.
		t.Cleanup(func() {
			_ = exec.Command("docker", "start", "skali-test").Run()
		})
	}
}
