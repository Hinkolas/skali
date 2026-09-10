// Blue-green live suite: the switch, the drain, a failing color, and the
// autoscaled hand-over against the k3d test cluster (task k3d:up), gated on
// TEST_KUBECONFIG plus TEST_DATABASE_URL like the rest of the live tests.
package reconcile

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	rendering "github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/module"
)

// liveManifestImage is liveManifest with a chosen image and a readiness
// probe: the probe's interval is the smallest window in which a new color
// is observably not yet available, so the hold can be asserted.
func liveManifestImage(project string, replicas int, autoscaled bool, image string) string {
	scaling := "    scaling:\n      replicas:\n        min: " + strconv.Itoa(replicas) + "\n"
	if autoscaled {
		scaling += "        max: " + strconv.Itoa(replicas+3) + "\n" +
			"      autoscaling:\n        cpu:\n          targetUtilization: 70\n"
	}
	return "version: \"1\"\nname: " + project + "\napplications:\n  web:\n" +
		"    image: " + image + "\n" +
		"    ports:\n      http:\n        port: 80\n        protocol: http\n" +
		"    health:\n      readiness:\n        http:\n          port: http\n          path: /health\n        interval: 10s\n" +
		scaling
}

func (f *liveFixture) webDeployments(t *testing.T) []appsv1.Deployment {
	t.Helper()
	list, err := f.clientset.AppsV1().Deployments(f.namespace).List(context.Background(), metav1.ListOptions{
		LabelSelector: rendering.LabelApplication + "=web",
	})
	require.NoError(t, err)
	return list.Items
}

func (f *liveFixture) webService(t *testing.T) *corev1.Service {
	t.Helper()
	service, err := f.clientset.CoreV1().Services(f.namespace).Get(context.Background(),
		rendering.ApplicationName(f.projectName, "web"), metav1.GetOptions{})
	require.NoError(t, err)
	return service
}

// webPodUIDs lists the pods of one web color; the failing color's pods may
// linger while they terminate, so comparisons stay within the serving color.
func (f *liveFixture) webPodUIDs(t *testing.T, color string) map[types.UID]bool {
	t.Helper()
	pods, err := f.clientset.CoreV1().Pods(f.namespace).List(context.Background(), metav1.ListOptions{
		LabelSelector: rendering.LabelApplication + "=web," + rendering.LabelColor + "=" + color,
	})
	require.NoError(t, err)
	uids := map[types.UID]bool{}
	for _, pod := range pods.Items {
		uids[pod.UID] = true
	}
	return uids
}

// A second revision starts its color beside the serving one, the Service
// keeps its selector until every new replica is available, then switches in
// one step, and the previous color is pruned after the drain window.
func TestLiveBlueGreenSwitchAndDrain(t *testing.T) {
	t.Parallel()
	f := newLiveFixture(t, Config{RolloutDeadline: 5 * time.Minute, RetireDrain: 5 * time.Second}, nil)
	f.start(t)

	first := f.deployManifest(t, liveManifestImage(f.projectName, 2, false, "traefik/whoami:v1.10.2"))
	f.waitActive(t, first.RevisionID, 3*time.Minute)
	old := f.webDeployment(t)
	oldColor := old.Labels[rendering.LabelColor]
	require.NotEmpty(t, oldColor, "the default strategy renders a colored Deployment")
	require.Equal(t, oldColor, f.webService(t).Spec.Selector[rendering.LabelColor])

	second := f.deployManifest(t, liveManifestImage(f.projectName, 2, false, "traefik/whoami:v1.10.1"))
	var pending *appsv1.Deployment
	require.Eventually(t, func() bool {
		for _, deployment := range f.webDeployments(t) {
			if deployment.Name != old.Name {
				pending = &deployment
				return true
			}
		}
		return false
	}, time.Minute, 200*time.Millisecond, "the new color must appear beside the old one")
	newColor := pending.Labels[rendering.LabelColor]
	require.NotEqual(t, oldColor, newColor)
	if pending.Status.AvailableReplicas < 2 {
		require.Equal(t, oldColor, f.webService(t).Spec.Selector[rendering.LabelColor],
			"traffic stays on the serving color while the new one is not fully available")
	}

	require.Eventually(t, func() bool {
		return f.webService(t).Spec.Selector[rendering.LabelColor] == newColor
	}, 3*time.Minute, 200*time.Millisecond, "the selector switches once the new color is available")
	switched := f.webDeployments(t)
	for _, deployment := range switched {
		if deployment.Name == pending.Name {
			require.EqualValues(t, 2, deployment.Status.AvailableReplicas, "the switch waits for full availability")
		}
	}
	f.waitActive(t, second.RevisionID, 2*time.Minute)

	require.Eventually(t, func() bool {
		deployments := f.webDeployments(t)
		return len(deployments) == 1 && deployments[0].Name == pending.Name
	}, time.Minute, 200*time.Millisecond, "the previous color is pruned after the drain window")
	require.Eventually(t, func() bool {
		status := f.status(t)
		return len(status.Services) == 1 && status.Services[0].Health == module.HealthHealthy
	}, time.Minute, 200*time.Millisecond)
}

// A color that never becomes available never receives traffic: the rollout
// deadline falls the target back, the failed Deployment is pruned, and the
// serving pods are the very same ones as before.
func TestLiveBlueGreenFailedColorNeverServes(t *testing.T) {
	t.Parallel()
	f := newLiveFixture(t, Config{RolloutDeadline: 30 * time.Second, RetireDrain: 5 * time.Second}, nil)
	f.start(t)
	ctx := context.Background()

	first := f.deployManifest(t, liveManifestImage(f.projectName, 1, false, "traefik/whoami:v1.10.2"))
	f.waitActive(t, first.RevisionID, 3*time.Minute)
	old := f.webDeployment(t)
	oldColor := old.Labels[rendering.LabelColor]
	before := f.webPodUIDs(t, oldColor)
	require.NotEmpty(t, before)

	failing := f.deployManifest(t, liveManifestImage(f.projectName, 1, false, "traefik/whoami:v0.0.0-does-not-exist"))
	require.Eventually(t, func() bool {
		return len(f.webDeployments(t)) == 2
	}, time.Minute, 200*time.Millisecond, "the failing color is applied beside the serving one")

	require.Eventually(t, func() bool {
		target, err := f.st.GetEnvironmentTarget(ctx, f.environmentID)
		return err == nil && target.TargetRevisionID != nil && *target.TargetRevisionID == first.RevisionID
	}, 3*time.Minute, 500*time.Millisecond, "the deadline falls the target back to the active revision")
	run, err := f.st.GetRunByID(ctx, failing.RunID)
	require.NoError(t, err)
	require.Equal(t, "failed", run.Status)

	require.Eventually(t, func() bool {
		deployments := f.webDeployments(t)
		return len(deployments) == 1 && deployments[0].Name == old.Name
	}, time.Minute, 200*time.Millisecond, "the failed color is pruned after the drain window")
	require.Equal(t, oldColor, f.webService(t).Spec.Selector[rendering.LabelColor], "traffic never moved")
	require.Equal(t, before, f.webPodUIDs(t, oldColor), "the serving pods were never touched")
}

// An autoscaled application hands its autoscaler over with the switch: the
// pending color is sized to the serving count and owned by skalid, the HPA
// targets the new color only once it serves, and the replica count never
// resets through the default on the way.
func TestLiveBlueGreenAutoscaledHandover(t *testing.T) {
	t.Parallel()
	f := newLiveFixture(t, Config{RolloutDeadline: 5 * time.Minute, RetireDrain: 5 * time.Second}, nil)
	f.start(t)
	ctx := context.Background()

	first := f.deployManifest(t, liveManifestImage(f.projectName, 2, true, "traefik/whoami:v1.10.2"))
	f.waitActive(t, first.RevisionID, 3*time.Minute)
	// A tap recording every replica count the API server serves for any web
	// Deployment from here on; a default reset during the switch would
	// surface as a 1. (A first autoscaled deploy starts at the Kubernetes
	// default before the autoscaler raises it, so the tap starts after it.)
	tapCtx, stopTap := context.WithCancel(ctx)
	defer stopTap()
	type replicaSample struct {
		name  string
		count int32
	}
	seen := make(chan replicaSample, 512)
	go func() {
		watcher, err := f.clientset.AppsV1().Deployments(f.namespace).Watch(tapCtx, metav1.ListOptions{
			LabelSelector: rendering.LabelApplication + "=web",
		})
		if err != nil {
			return
		}
		defer watcher.Stop()
		for event := range watcher.ResultChan() {
			if deployment, ok := event.Object.(*appsv1.Deployment); ok && deployment.Spec.Replicas != nil {
				select {
				case seen <- replicaSample{deployment.Name, *deployment.Spec.Replicas}:
				default:
				}
			}
		}
	}()

	old := f.webDeployment(t)
	hpaName := rendering.ApplicationName(f.projectName, "web")
	hpa, err := f.clientset.AutoscalingV2().HorizontalPodAutoscalers(f.namespace).Get(ctx, hpaName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, old.Name, hpa.Spec.ScaleTargetRef.Name)

	second := f.deployManifest(t, liveManifestImage(f.projectName, 2, true, "traefik/whoami:v1.10.1"))
	var pending *appsv1.Deployment
	require.Eventually(t, func() bool {
		for _, deployment := range f.webDeployments(t) {
			if deployment.Name != old.Name {
				pending = &deployment
				return true
			}
		}
		return false
	}, time.Minute, 200*time.Millisecond)
	require.NotNil(t, pending.Spec.Replicas, "a pending autoscaled color carries an explicit count")
	require.EqualValues(t, 2, *pending.Spec.Replicas, "sized to the serving count")
	if pending.Status.AvailableReplicas < 2 {
		hpa, err = f.clientset.AutoscalingV2().HorizontalPodAutoscalers(f.namespace).Get(ctx, hpaName, metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, old.Name, hpa.Spec.ScaleTargetRef.Name, "the autoscaler steers the serving color until the switch")
	}

	f.waitActive(t, second.RevisionID, 3*time.Minute)
	require.Eventually(t, func() bool {
		hpa, err := f.clientset.AutoscalingV2().HorizontalPodAutoscalers(f.namespace).Get(ctx, hpaName, metav1.GetOptions{})
		return err == nil && hpa.Spec.ScaleTargetRef.Name == pending.Name
	}, time.Minute, 200*time.Millisecond, "the autoscaler follows the switch")
	require.Eventually(t, func() bool {
		live, err := f.clientset.AppsV1().Deployments(f.namespace).Get(ctx, pending.Name, metav1.GetOptions{})
		return err == nil && !kubeOwnsReplicas(live)
	}, time.Minute, 200*time.Millisecond, "skalid releases the replica field to the autoscaler after the switch")
	require.Eventually(t, func() bool {
		deployments := f.webDeployments(t)
		return len(deployments) == 1 && deployments[0].Name == pending.Name
	}, time.Minute, 200*time.Millisecond)

	stopTap()
	close(seen)
	// The new color must never drop below the count it was sized with: a
	// reset through the default would read 1. The previous color is left to
	// its own autoscaler until it retires, so its samples carry no claim.
	for sample := range seen {
		if sample.name == pending.Name {
			require.GreaterOrEqual(t, sample.count, int32(2), "the new color must never reset through the default")
		}
	}
}

// kubeOwnsReplicas reports whether skalid's apply entry still owns
// spec.replicas on a live Deployment.
func kubeOwnsReplicas(deployment *appsv1.Deployment) bool {
	for _, entry := range deployment.ManagedFields {
		if entry.Manager != "skalid-project" || entry.FieldsV1 == nil {
			continue
		}
		if strings.Contains(string(entry.FieldsV1.Raw), `"f:replicas"`) {
			return true
		}
	}
	return false
}
