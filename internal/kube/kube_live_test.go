package kube_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubetest"
)

func liveDeployment(namespace string, replicas int32) *appsv1.Deployment {
	labels := map[string]string{"app": "smoke", "skali.dev/managed": "true"}
	return &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Name: "smoke", Namespace: namespace, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "smoke"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name:  "smoke",
					Image: "traefik/whoami:v1.10.2",
				}}},
			},
		},
	}
}

// Live smoke: SSA apply is idempotent, and replicas ownership can be
// released and verified through the real API server.
func TestLiveApplyIdempotenceAndDisown(t *testing.T) {
	t.Parallel()
	config := kubetest.Config(t)
	client, err := kube.NewFromConfig(config)
	require.NoError(t, err)
	clientset := kubetest.Clientset(t)
	namespace := kubetest.Namespace(t, clientset)
	ctx := context.Background()

	first, err := client.Apply(ctx, liveDeployment(namespace, 2), false)
	require.NoError(t, err)
	require.True(t, first.Changed)

	second, err := client.Apply(ctx, liveDeployment(namespace, 2), false)
	require.NoError(t, err)
	require.False(t, second.Changed, "an unchanged configuration must be a no-op apply")

	require.True(t, kube.OwnsField(second.Live.GetManagedFields(), kube.FieldManagerProject, kube.ReplicasFieldPath))

	ref := kube.ObjectRef{
		GVK:       appsv1.SchemeGroupVersion.WithKind("Deployment"),
		Namespace: namespace,
		Name:      "smoke",
	}
	require.NoError(t, client.DisownFields(ctx, ref, kube.FieldManagerProject, kube.ReplicasFieldPath))

	live, err := clientset.AppsV1().Deployments(namespace).Get(ctx, "smoke", metav1.GetOptions{})
	require.NoError(t, err)
	require.False(t, kube.OwnsField(live.GetManagedFields(), kube.FieldManagerProject, kube.ReplicasFieldPath))
	require.NotNil(t, live.Spec.Replicas)
	require.Equal(t, int32(2), *live.Spec.Replicas, "disowning must not move the live value")

	// Deleting with a stale UID precondition is refused; the real UID works.
	staleRef := ref
	staleRef.UID = "00000000-0000-0000-0000-000000000000"
	_, err = client.Delete(ctx, staleRef)
	require.Error(t, err)
	ref.UID = live.UID
	deleted, err := client.Delete(ctx, ref)
	require.NoError(t, err)
	require.True(t, deleted)
	deleted, err = client.Delete(ctx, ref)
	require.NoError(t, err, "deleting an absent object is success")
	require.False(t, deleted)
}
