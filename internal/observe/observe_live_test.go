package observe_test

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubetest"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/observe"
)

func labeledDeployment(namespace string, envID uuid.UUID, replicas int32) *appsv1.Deployment {
	selector := map[string]string{"app": "observed"}
	labels := map[string]string{
		"app":                   "observed",
		"skali.dev/managed":     "true",
		"skali.dev/project":     "observe-test",
		"skali.dev/application": "observed",
		"skali.dev/service":     "observed",
		"skali.dev/environment": envID.String(),
		"skali.dev/revision":    "feedfacefeedface",
	}
	return &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Name: "observed", Namespace: namespace, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: selector},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name:  "observed",
					Image: "traefik/whoami:v1.10.2",
				}}},
			},
		},
	}
}

func startSource(t *testing.T, config *rest.Config, threshold time.Duration) (*kube.Client, *observe.Store) {
	t.Helper()
	client, err := kube.NewFromConfig(config)
	require.NoError(t, err)
	store := observe.NewStore(nil)
	source := observe.NewKubeSource(client, store, observe.SourceOptions{
		Resync:         time.Hour, // watches, not resync, must explain every propagation
		StaleThreshold: threshold,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = source.Run(ctx) }()
	require.Eventually(t, store.Ready, 30*time.Second, 100*time.Millisecond,
		"cache must synchronize")
	return client, store
}

func waitForWorkload(t *testing.T, store *observe.Store, envID uuid.UUID, check func(module.WorkloadStatus) bool) {
	t.Helper()
	require.Eventually(t, func() bool {
		for _, obj := range store.Snapshot(envID).Objects {
			if obj.Kind == module.KindWorkload && obj.Workload != nil && check(*obj.Workload) {
				return true
			}
		}
		return false
	}, 60*time.Second, 200*time.Millisecond)
}

// Live: LIST/WATCH feeds the store; a spec change propagates through the
// watch (resync is an hour away).
func TestLiveListWatchPropagation(t *testing.T) {
	t.Parallel()
	config := kubetest.Config(t)
	clientset := kubetest.Clientset(t)
	namespace := kubetest.Namespace(t, clientset)
	envID := uuid.New()
	client, store := startSource(t, config, 30*time.Second)
	ctx := context.Background()

	_, err := client.Apply(ctx, labeledDeployment(namespace, envID, 1), false)
	require.NoError(t, err)
	waitForWorkload(t, store, envID, func(w module.WorkloadStatus) bool { return w.Desired == 1 })

	_, err = client.Apply(ctx, labeledDeployment(namespace, envID, 2), false)
	require.NoError(t, err)
	waitForWorkload(t, store, envID, func(w module.WorkloadStatus) bool { return w.Desired == 2 })

	// Pods appear with node placement and revision identity.
	require.Eventually(t, func() bool {
		pods := 0
		for _, obj := range store.Snapshot(envID).Objects {
			if obj.Kind == module.KindPod && obj.Revision == "feedfacefeedface" {
				pods++
			}
		}
		return pods == 2
	}, 60*time.Second, 200*time.Millisecond, "pods must be observed with identity labels")

	// The node informer records the cluster platform; the k3d node runs on
	// the host architecture.
	require.Contains(t, store.NodePlatforms(), "linux/"+runtime.GOARCH,
		"node architectures must be observed")
}

// Exit criterion: watch disconnect is visible as stale/unknown and recovers
// cleanly.
func TestLiveWatchDisconnectStaleAndRecover(t *testing.T) {
	t.Parallel()
	config, proxy := kubetest.ProxiedConfig(t)
	clientset := kubetest.Clientset(t) // direct, unproxied control connection
	namespace := kubetest.Namespace(t, clientset)
	envID := uuid.New()
	threshold := 3 * time.Second
	client, store := startSource(t, config, threshold)
	ctx := context.Background()

	_, err := client.Apply(ctx, labeledDeployment(namespace, envID, 1), false)
	require.NoError(t, err)
	waitForWorkload(t, store, envID, func(w module.WorkloadStatus) bool { return w.Desired == 1 })
	require.Equal(t, module.SourceFresh, store.Source().State)

	proxy.Sever()
	_, verr := client.Clientset.Discovery().ServerVersion()
	require.Error(t, verr, "severed proxy must block requests")
	require.Eventually(t, func() bool {
		return store.Source().State == module.SourceStale
	}, 30*time.Second, 200*time.Millisecond, "severed watches must surface as stale")
	require.False(t, store.Source().StaleSince.IsZero())

	proxy.Resume()
	require.Eventually(t, func() bool {
		return store.Source().State == module.SourceFresh
	}, 60*time.Second, 200*time.Millisecond, "restored connectivity must recover to fresh")

	// The recovered watch still propagates changes (scale via the direct
	// connection; observe through the proxied one).
	scale, err := clientset.AppsV1().Deployments(namespace).GetScale(ctx, "observed", metav1.GetOptions{})
	require.NoError(t, err)
	scale.Spec.Replicas = 3
	_, err = clientset.AppsV1().Deployments(namespace).UpdateScale(ctx, "observed", scale, metav1.UpdateOptions{})
	require.NoError(t, err)
	waitForWorkload(t, store, envID, func(w module.WorkloadStatus) bool { return w.Desired == 3 })
}
