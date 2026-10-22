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
	// Image is the postgres container's image: what a patch update
	// waits on until every member runs the pool's.
	Image string
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
		for _, container := range pod.Spec.Containers {
			if container.Name == PostgresContainer {
				instance.Image = container.Image
			}
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

// HealthyPhase is CNPG's steady-state Cluster phase.
const HealthyPhase = "Cluster in healthy state"

// Settled reports whether a pool runs image throughout, with the reason
// when it does not: the Cluster in its steady-state phase and every member
// ready with its postgres container on image. A pool CNPG has not started
// rolling yet still has members on the previous image, so a changed image
// reads as unsettled from the moment it is applied until the last member
// runs it.
func Settled(phase string, members []Instance, image string) (bool, string) {
	if phase != HealthyPhase {
		return false, phase
	}
	if len(members) == 0 {
		return false, "no members"
	}
	for _, member := range members {
		if member.Image != image {
			return false, member.Name + " runs " + member.Image
		}
		if !member.Ready {
			return false, member.Name + " is not ready"
		}
	}
	return true, ""
}
