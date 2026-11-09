package bundle

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/Hinkolas/skali/internal/kube"
)

func traefikDaemonSet(args []string, desired, updated, available int32) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "traefik", Namespace: "kube-system", Generation: 3},
		Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "traefik", Args: args}},
		}}},
		Status: appsv1.DaemonSetStatus{
			ObservedGeneration: 3, DesiredNumberScheduled: desired,
			UpdatedNumberScheduled: updated, NumberAvailable: available,
		},
	}
}

func TestEdgeTraefikPending(t *testing.T) {
	t.Parallel()
	trusted := []string{"10.0.0.2/32", "10.0.0.3/32"}
	both := []string{
		"--entryPoints.web.proxyProtocol.trustedIPs=10.0.0.2/32,10.0.0.3/32",
		"--entryPoints.websecure.proxyProtocol.trustedIPs=10.0.0.2/32,10.0.0.3/32",
	}

	require.Empty(t, edgeTraefikPending(traefikDaemonSet(both, 2, 2, 2), trusted))
	require.Empty(t, edgeTraefikPending(traefikDaemonSet(nil, 1, 1, 1), nil), "off and rolled out")

	require.Contains(t, edgeTraefikPending(traefikDaemonSet(nil, 2, 2, 2), trusted), "does not yet trust",
		"the helm job has not re-rendered the overlay yet")
	require.Contains(t, edgeTraefikPending(traefikDaemonSet(both[:1], 2, 2, 2), trusted), "websecure")
	stale := []string{"--entryPoints.web.proxyProtocol.trustedIPs=10.0.0.9/32", both[1]}
	require.Contains(t, edgeTraefikPending(traefikDaemonSet(stale, 2, 2, 2), trusted), "on web")
	require.Contains(t, edgeTraefikPending(traefikDaemonSet(both, 2, 2, 2), nil), "still trusts",
		"turning PROXY protocol off waits for the trust to disappear")

	require.Contains(t, edgeTraefikPending(traefikDaemonSet(both, 2, 1, 2), trusted), "1 of 2")
	require.Contains(t, edgeTraefikPending(traefikDaemonSet(both, 2, 2, 1), trusted), "rolling out")
	require.Contains(t, edgeTraefikPending(traefikDaemonSet(both, 0, 0, 0), trusted), "no edge node")
	unobserved := traefikDaemonSet(both, 2, 2, 2)
	unobserved.Status.ObservedGeneration = 2
	require.Contains(t, edgeTraefikPending(unobserved, trusted), "not observed")
}

func TestWaitEdgeTraefik(t *testing.T) {
	previous := edgeWaitInterval
	edgeWaitInterval = 10 * time.Millisecond
	t.Cleanup(func() { edgeWaitInterval = previous })
	ctx := context.Background()
	profile := productionProfile()
	profile.Production.ProxyProtocolTrustedIPs = []string{"10.0.0.2/32"}

	ready := traefikDaemonSet([]string{
		"--entryPoints.web.proxyProtocol.trustedIPs=10.0.0.2/32",
		"--entryPoints.websecure.proxyProtocol.trustedIPs=10.0.0.2/32",
	}, 1, 1, 1)
	require.NoError(t, WaitEdgeTraefik(ctx, &kube.Client{Clientset: fake.NewSimpleClientset(ready)}, profile, time.Second))

	// The old single-replica Deployment is all there is until the helm job
	// runs: the wait times out with the reason instead of succeeding.
	err := WaitEdgeTraefik(ctx, &kube.Client{Clientset: fake.NewSimpleClientset()}, profile, 50*time.Millisecond)
	require.ErrorContains(t, err, "does not exist yet")
	require.ErrorContains(t, err, "do not enable PROXY protocol")
}
