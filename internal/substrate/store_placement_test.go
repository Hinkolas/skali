package substrate

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
)

func servingPod(name, node string) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       corev1.PodSpec{NodeName: node},
		Status: corev1.PodStatus{Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}
}

// TestSharedNodes: two filers on two nodes must sit apart, so both on
// one node is a shortfall; three masters on two surviving nodes cannot
// do better than two and one, so that is not; pods that are leaving,
// pending, or not ready do not count, which keeps a rollout's surge pod
// out of the picture until it serves.
func TestSharedNodes(t *testing.T) {
	t.Parallel()
	apart := []corev1.Pod{servingPod("filer-a", "node-a"), servingPod("filer-b", "node-b")}
	require.Empty(t, sharedNodes("filer", apart, 2, 2))

	together := []corev1.Pod{servingPod("filer-a", "node-a"), servingPod("filer-b", "node-a")}
	require.Equal(t, []module.ComponentPlacement{{Component: "filer", Node: "node-a", Pods: 2}},
		sharedNodes("filer", together, 2, 2))

	twoNodesLeft := []corev1.Pod{servingPod("master-0", "node-a"), servingPod("master-1", "node-a"), servingPod("master-2", "node-b")}
	require.Empty(t, sharedNodes("master", twoNodesLeft, 3, 2), "two on one node is the best two nodes can do")
	require.Equal(t, []module.ComponentPlacement{{Component: "master", Node: "node-a", Pods: 2}},
		sharedNodes("master", twoNodesLeft, 3, 3), "with three nodes they must spread")

	leaving := servingPod("filer-c", "node-a")
	now := metav1.NewTime(time.Now())
	leaving.DeletionTimestamp = &now
	pending := servingPod("filer-d", "node-a")
	pending.Status.Phase = corev1.PodPending
	notReady := servingPod("filer-e", "node-a")
	notReady.Status.Conditions[0].Status = corev1.ConditionFalse
	unscheduled := servingPod("filer-f", "")
	require.Empty(t, sharedNodes("filer", []corev1.Pod{servingPod("filer-a", "node-a"), leaving, pending, notReady, unscheduled}, 2, 2))
}

// TestObjectStorageNodesDriveFilerCount: the filer count follows the
// object-storage nodes the cluster has, not their momentary readiness, so
// a node outage does not resize the gateway tier, and nodes without the
// capability never count, since filers cannot run there; a controller
// without node observations renders the single-node shape.
func TestObjectStorageNodesDriveFilerCount(t *testing.T) {
	t.Parallel()
	bare := &Controller{cfg: Config{Managed: true}}
	require.Equal(t, 0, bare.objectStorageNodes())
	require.Equal(t, 1, seaweed.FilersForNodes(bare.objectStorageNodes()))

	observed := observe.NewStore(nil)
	observed.SetNodeRecord(observe.NodeRecord{Name: "node-a", Ready: true, Schedulable: true})
	observed.SetNodeCapabilities("node-a", []string{layout.CapabilityObjectStorage})
	controller := &Controller{cfg: Config{Managed: true}, deps: Deps{Observed: observed}}
	require.Equal(t, 1, seaweed.FilersForNodes(controller.objectStorageNodes()))

	observed.SetNodeRecord(observe.NodeRecord{Name: "registry", Ready: true, Schedulable: true})
	observed.SetNodeCapabilities("registry", []string{layout.CapabilityRegistry})
	require.Equal(t, 1, seaweed.FilersForNodes(controller.objectStorageNodes()), "a node without the capability holds no filer")

	observed.SetNodeRecord(observe.NodeRecord{Name: "node-b", Ready: false, Schedulable: true})
	observed.SetNodeCapabilities("node-b", []string{layout.CapabilityObjectStorage})
	require.Equal(t, 2, seaweed.FilersForNodes(controller.objectStorageNodes()), "a node in an outage keeps its place")

	observed.RemoveNode("node-b")
	require.Equal(t, 1, seaweed.FilersForNodes(controller.objectStorageNodes()), "a removed node leaves the shape")
}
