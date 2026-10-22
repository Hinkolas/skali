package bundle

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubetest"
)

// A converge over a Deployment the previous release rolled moves it to
// the Recreate strategy on a real API server, which keeps the defaulted
// rollingUpdate parameters server-side apply cannot drop, and the switch
// itself rolls nothing. Requires TEST_KUBECONFIG.
func TestLiveApplySwitchesDeploymentToRecreate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client, err := kube.NewFromConfig(kubetest.Config(t))
	require.NoError(t, err)
	namespace := kubetest.Namespace(t, client.Clientset)
	applier := &Applier{Client: client, PollInterval: time.Second, WaitTimeout: 2 * time.Minute}

	rolling := strategyDeployment(namespace, nil)
	require.NoError(t, applier.ApplyObjects(ctx, []unstructured.Unstructured{rolling}))
	require.NoError(t, applier.WaitDeploymentReady(ctx, namespace, rolling.GetName()))
	before, err := client.Clientset.AppsV1().Deployments(namespace).Get(ctx, rolling.GetName(), metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "RollingUpdate", string(before.Spec.Strategy.Type))
	require.NotNil(t, before.Spec.Strategy.RollingUpdate, "the API server defaulted the parameters")

	recreate := strategyDeployment(namespace, map[string]any{"type": "Recreate"})
	require.NoError(t, applier.ApplyObjects(ctx, []unstructured.Unstructured{recreate}))
	after, err := client.Clientset.AppsV1().Deployments(namespace).Get(ctx, recreate.GetName(), metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "Recreate", string(after.Spec.Strategy.Type))
	require.Nil(t, after.Spec.Strategy.RollingUpdate)
	sets, err := client.Clientset.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, sets.Items, 1, "switching the strategy started no rollout")

	// The next converge is a plain apply again.
	require.NoError(t, applier.ApplyObjects(ctx, []unstructured.Unstructured{recreate}))
}

func strategyDeployment(namespace string, strategy map[string]any) unstructured.Unstructured {
	labels := map[string]any{"app.kubernetes.io/name": "strategy-switch"}
	spec := map[string]any{
		"replicas": int64(1),
		"selector": map[string]any{"matchLabels": labels},
		"template": map[string]any{
			"metadata": map[string]any{"labels": labels},
			"spec": map[string]any{
				"terminationGracePeriodSeconds": int64(1),
				"containers": []any{map[string]any{
					"name": "pause", "image": "registry.k8s.io/pause:3.10",
				}},
			},
		},
	}
	if strategy != nil {
		spec["strategy"] = strategy
	}
	return unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "strategy-switch", "namespace": namespace},
		"spec":     spec,
	}}
}
