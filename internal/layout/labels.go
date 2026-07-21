package layout

import "slices"

// Node labels stamped by skali-installer at install/join time. They are the
// durable, cluster-readable form of the layout: `skali-installer init` and
// later skalid rebuild node capabilities from these labels instead of
// trusting a document on disk.
const (
	// CapabilityLabelPrefix prefixes one node label per designated
	// capability: a capability-carrying node holds
	// skali.dev/capability-<name>: "true". The skali.dev/ namespace matches
	// the ownership labels in internal/kubernetes.
	CapabilityLabelPrefix = "skali.dev/capability-"

	// CapabilityLabelValue is the only value a capability label carries;
	// absence of the label is the negative.
	CapabilityLabelValue = "true"

	// ClusterLabel records which installation stamped the node.
	ClusterLabel = "skali.dev/cluster"

	// ControlPlaneLabel is k3s's own role marker. The K3s role is never
	// duplicated into a skali label because k3s already owns that fact.
	ControlPlaneLabel = "node-role.kubernetes.io/control-plane"
)

// CapabilityLabel returns the node label for one capability, for example
// skali.dev/capability-registry for CapabilityRegistry.
func CapabilityLabel(capability string) string {
	return CapabilityLabelPrefix + capability
}

// CapabilityLabels returns the label set the installer stamps for the given
// capabilities.
func CapabilityLabels(capabilities []string) map[string]string {
	labels := make(map[string]string, len(capabilities))
	for _, capability := range capabilities {
		labels[CapabilityLabel(capability)] = CapabilityLabelValue
	}
	return labels
}

// CapabilitiesFromLabels rebuilds the capability set from node labels, in
// Capabilities display order. Unknown skali.dev/capability- labels are
// ignored: an older installer reading a newer node reports what it knows.
func CapabilitiesFromLabels(labels map[string]string) []string {
	var capabilities []string
	for _, capability := range Capabilities {
		if labels[CapabilityLabel(capability)] == CapabilityLabelValue {
			capabilities = append(capabilities, capability)
		}
	}
	return capabilities
}

// RoleFromLabels derives the K3s role from k3s's own control-plane marker.
func RoleFromLabels(labels map[string]string) string {
	if _, ok := labels[ControlPlaneLabel]; ok {
		return RoleServer
	}
	return RoleAgent
}

// UnionCapabilities returns the union of every node's capabilities in
// Capabilities display order; it feeds SKALI_CAPABILITIES for the
// installation.
func UnionCapabilities(nodes map[string]Node) []string {
	seen := make(map[string]bool)
	for _, node := range nodes {
		for _, capability := range node.Capabilities {
			seen[capability] = true
		}
	}
	var union []string
	for _, capability := range Capabilities {
		if seen[capability] {
			union = append(union, capability)
		}
	}
	// Unknown capabilities would only appear through a newer document
	// version; keep them stable rather than dropping them silently.
	var unknown []string
	for capability := range seen {
		if !slices.Contains(Capabilities, capability) {
			unknown = append(unknown, capability)
		}
	}
	slices.Sort(unknown)
	return append(union, unknown...)
}
