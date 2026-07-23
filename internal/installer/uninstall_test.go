package installer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/layout"
)

func clusterNode(name string, server bool) *corev1.Node {
	labels := map[string]string{}
	if server {
		labels[layout.ControlPlaneLabel] = "true"
	}
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

func systemPod(name, nodeName string, pvc bool) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: bundle.Namespace},
		Spec:       corev1.PodSpec{NodeName: nodeName},
	}
	if pvc {
		pod.Spec.Volumes = []corev1.Volume{{
			Name: "data",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: name},
			},
		}}
	}
	return pod
}

func TestPlanNodeRemovalGuards(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// A healthy pair: removing one server is allowed and counted.
	client := &kube.Client{Clientset: k8sfake.NewSimpleClientset(
		clusterNode("cp-1", true), clusterNode("cp-2", true), clusterNode("db-1", false),
	)}
	plan, err := planNodeRemovalWith(ctx, client,
		&NodeRemovalPlan{NodeName: "cp-2", Role: layout.RoleServer, Total: 1})
	require.NoError(t, err)
	require.Equal(t, 3, plan.Total)
	require.Equal(t, 2, plan.Servers)
	require.Equal(t, 1, plan.Agents)

	// The last server never leaves while agents remain.
	client = &kube.Client{Clientset: k8sfake.NewSimpleClientset(
		clusterNode("cp-1", true), clusterNode("db-1", false),
	)}
	_, err = planNodeRemovalWith(ctx, client,
		&NodeRemovalPlan{NodeName: "cp-1", Role: layout.RoleServer, Total: 1})
	require.ErrorContains(t, err, "this is the only server")

	// skali-system data on the leaving node blocks until relocated; pods
	// without claims and pods on other nodes do not.
	client = &kube.Client{Clientset: k8sfake.NewSimpleClientset(
		clusterNode("cp-1", true), clusterNode("cp-2", true),
		systemPod("skali-db-1", "cp-2", true),
		systemPod("skalid-abc", "cp-2", false),
		systemPod("skali-db-2", "cp-1", true),
	)}
	_, err = planNodeRemovalWith(ctx, client,
		&NodeRemovalPlan{NodeName: "cp-2", Role: layout.RoleServer, Total: 1})
	require.ErrorContains(t, err, "skali-system data lives on this node (skali-db-1)")

	// A single node passes with no guard: destroying the cluster is the
	// separately confirmed path.
	client = &kube.Client{Clientset: k8sfake.NewSimpleClientset(clusterNode("cp-1", true))}
	plan, err = planNodeRemovalWith(ctx, client,
		&NodeRemovalPlan{NodeName: "cp-1", Role: layout.RoleServer, Total: 1})
	require.NoError(t, err)
	require.Equal(t, 1, plan.Total)
}
