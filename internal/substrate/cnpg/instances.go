package cnpg

import (
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// Labels CloudNativePG stamps on every instance pod. The role label was
// renamed in CNPG 1.23; the legacy name stays as a fallback for pods an
// older operator created.
const (
	LabelCluster      = "cnpg.io/cluster"
	LabelPodRole      = "cnpg.io/podRole"
	LabelInstanceRole = "cnpg.io/instanceRole"
	LabelRoleLegacy   = "cnpg.io/role"

	PodRoleInstance = "instance"
	RolePrimary     = "primary"
	RoleReplica     = "replica"
)

// InstanceSelector selects one pool's instance pods (never its jobs).
func InstanceSelector(pool string) string {
	return LabelCluster + "=" + pool + "," + LabelPodRole + "=" + PodRoleInstance
}

// Instance is one pool member as its pod reports it.
type Instance struct {
	Name string
	// Role is primary or replica; empty while CNPG has not labeled the pod.
	Role      string
	Node      string
	Ready     bool
	Restarts  int32
	StartedAt *time.Time
	Phase     string
}

// InstancesFromPods projects instance pods into members, the primary
// first and the rest by name.
func InstancesFromPods(pods []corev1.Pod) []Instance {
	out := make([]Instance, 0, len(pods))
	for _, pod := range pods {
		instance := Instance{
			Name:  pod.Name,
			Role:  pod.Labels[LabelInstanceRole],
			Node:  pod.Spec.NodeName,
			Phase: string(pod.Status.Phase),
		}
		if instance.Role == "" {
			instance.Role = pod.Labels[LabelRoleLegacy]
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady {
				instance.Ready = condition.Status == corev1.ConditionTrue
			}
		}
		for _, status := range pod.Status.ContainerStatuses {
			instance.Restarts += status.RestartCount
		}
		if pod.Status.StartTime != nil {
			started := pod.Status.StartTime.Time
			instance.StartedAt = &started
		}
		out = append(out, instance)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Role == RolePrimary) != (out[j].Role == RolePrimary) {
			return out[i].Role == RolePrimary
		}
		return out[i].Name < out[j].Name
	})
	return out
}
