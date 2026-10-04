package substrate

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"

	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
)

// objectStorageNodes counts the nodes carrying the object-storage
// capability, the domains every store component spreads across. Every
// labelled node counts, ready or not: a node in a transient outage keeps
// its place in the shape (its pods are replaced on the survivors, taints
// are honored by the spread rule) and the count only moves when a node
// joins, is removed, or changes its capabilities.
func (c *Controller) objectStorageNodes() int {
	if c.deps.Observed == nil {
		return 0
	}
	return len(c.deps.Observed.CapableNodes(layout.CapabilityObjectStorage))
}

// placementShortfalls reports the store components whose pods share a
// node although the fleet has room to spread them. The spread rule
// prevents it at scheduling time; a replacement scheduled while a node
// was down lands on a survivor, and nothing moves it back when the node
// returns, so the shortfall persists silently until the probe names it.
// Only components with more than one replica and more than one eligible
// node can fall short; masters and filers both spread over the
// object-storage nodes.
func (c *Controller) placementShortfalls(ctx context.Context, row store.ObjectStore) ([]module.ComponentPlacement, error) {
	if c.deps.Observed == nil || c.deps.Cluster == nil {
		return nil, nil
	}
	capable := c.objectStorageNodes()
	components := []struct {
		app, name         string
		desired, eligible int
	}{
		{seaweed.MasterService, "master", int(row.Masters), capable},
		{seaweed.FilerService, "filer", seaweed.FilersForNodes(capable), capable},
	}
	var shortfalls []module.ComponentPlacement
	for _, component := range components {
		if component.desired < 2 || component.eligible < 2 {
			continue
		}
		pods, err := c.deps.Cluster.ListPods(ctx, Namespace, "app="+component.app)
		if err != nil {
			return nil, fmt.Errorf("substrate: list %s pods: %w", component.name, err)
		}
		shortfalls = append(shortfalls, sharedNodes(component.name, pods, component.desired, component.eligible)...)
	}
	return shortfalls, nil
}

// sharedNodes names the nodes carrying more of a component's serving pods
// than an even spread of the desired count over the eligible nodes
// allows: two filers on two nodes allow one per node, three masters on
// two surviving nodes allow two on one. Only running, ready pods count,
// so a rollout's surge pod registers only in the moment before the pod
// it replaces leaves.
func sharedNodes(component string, pods []corev1.Pod, desired, eligible int) []module.ComponentPlacement {
	perNode := map[string]int32{}
	for _, pod := range pods {
		if pod.Spec.NodeName == "" || pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning || !podReady(pod) {
			continue
		}
		perNode[pod.Spec.NodeName]++
	}
	allowed := int32((desired + eligible - 1) / eligible)
	var shortfalls []module.ComponentPlacement
	for node, count := range perNode {
		if count > allowed {
			shortfalls = append(shortfalls, module.ComponentPlacement{Component: component, Node: node, Pods: count})
		}
	}
	sort.Slice(shortfalls, func(i, j int) bool { return shortfalls[i].Node < shortfalls[j].Node })
	return shortfalls
}

func podReady(pod corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}
