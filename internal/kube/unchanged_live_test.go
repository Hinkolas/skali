package kube_test

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubetest"
)

type patchCounter struct {
	base    http.RoundTripper
	patches *atomic.Int64
}

func (c patchCounter) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method == http.MethodPatch {
		c.patches.Add(1)
	}
	return c.base.RoundTrip(request)
}

// Live: an apply that would change nothing is not sent, and every apply
// that would change something is: a changed value, and a dropped field the
// server then prunes. Another manager's field is not the configuration's
// concern, and a forced apply is always sent.
func TestLiveApplySendsOnlyChanges(t *testing.T) {
	t.Parallel()
	config := kubetest.Config(t)
	var patches atomic.Int64
	config.Wrap(func(base http.RoundTripper) http.RoundTripper {
		return patchCounter{base: base, patches: &patches}
	})
	client, err := kube.NewFromConfig(config)
	require.NoError(t, err)
	clientset := kubetest.Clientset(t)
	namespace := kubetest.Namespace(t, clientset)
	ctx := context.Background()
	apply := func(deployment *appsv1.Deployment, force bool) bool {
		t.Helper()
		before := patches.Load()
		_, err := client.Apply(ctx, deployment, force)
		require.NoError(t, err)
		return patches.Load() > before
	}
	labelled := func(replicas int32) *appsv1.Deployment {
		deployment := liveDeployment(namespace, replicas)
		deployment.Labels = map[string]string{"app": "smoke", "skali.dev/managed": "true", "tier": "web"}
		return deployment
	}

	require.True(t, apply(labelled(2), false), "creating is sent")
	require.False(t, apply(labelled(2), false), "an unchanged configuration is not sent")

	_, err = clientset.AppsV1().Deployments(namespace).Patch(ctx, "smoke", types.MergePatchType,
		[]byte(`{"metadata":{"annotations":{"example.com/other":"value"}}}`), metav1.PatchOptions{FieldManager: "other"})
	require.NoError(t, err)
	require.False(t, apply(labelled(2), false), "another manager's field changes nothing applied")

	require.True(t, apply(labelled(3), false), "a changed value is sent")
	require.False(t, apply(labelled(3), false))

	require.True(t, apply(liveDeployment(namespace, 3), false), "a dropped field is sent")
	live, err := clientset.AppsV1().Deployments(namespace).Get(ctx, "smoke", metav1.GetOptions{})
	require.NoError(t, err)
	require.NotContains(t, live.Labels, "tier", "the server prunes the dropped field")
	require.Equal(t, "value", live.Annotations["example.com/other"])
	require.False(t, apply(liveDeployment(namespace, 3), false))

	require.True(t, apply(liveDeployment(namespace, 3), true), "a forced apply is always sent")
}
