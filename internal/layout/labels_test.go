package layout

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCapabilityLabelStrings(t *testing.T) {
	t.Parallel()
	// The label strings are API once stamped on nodes; pin them.
	require.Equal(t, map[string]string{
		CapabilityApplication:   "skali.dev/capability-application",
		CapabilityDatabase:      "skali.dev/capability-database",
		CapabilityObjectStorage: "skali.dev/capability-object-storage",
		CapabilityRegistry:      "skali.dev/capability-registry",
		CapabilityEdge:          "skali.dev/capability-edge",
	}, func() map[string]string {
		labels := make(map[string]string)
		for _, capability := range Capabilities {
			labels[capability] = CapabilityLabel(capability)
		}
		return labels
	}())
	require.Equal(t, "skali.dev/cluster", ClusterLabel)
}

func TestCapabilityLabelsRoundTrip(t *testing.T) {
	t.Parallel()
	// Every subset of the capability vocabulary survives the label round
	// trip in display order.
	for mask := 0; mask < 1<<len(Capabilities); mask++ {
		var subset []string
		for index, capability := range Capabilities {
			if mask&(1<<index) != 0 {
				subset = append(subset, capability)
			}
		}
		require.Equal(t, subset, CapabilitiesFromLabels(CapabilityLabels(subset)), "mask %b", mask)
	}
}

func TestCapabilitiesFromLabelsIgnoresForeign(t *testing.T) {
	t.Parallel()
	labels := CapabilityLabels([]string{CapabilityDatabase})
	labels["skali.dev/capability-future-thing"] = "true"
	labels["kubernetes.io/hostname"] = "cp-1"
	labels[CapabilityLabel(CapabilityEdge)] = "false"
	require.Equal(t, []string{CapabilityDatabase}, CapabilitiesFromLabels(labels))
}

func TestRoleFromLabels(t *testing.T) {
	t.Parallel()
	require.Equal(t, RoleServer, RoleFromLabels(map[string]string{ControlPlaneLabel: "true"}))
	require.Equal(t, RoleServer, RoleFromLabels(map[string]string{ControlPlaneLabel: ""}))
	require.Equal(t, RoleAgent, RoleFromLabels(map[string]string{"kubernetes.io/hostname": "db-1"}))
}

func TestUnionCapabilities(t *testing.T) {
	t.Parallel()
	nodes := map[string]Node{
		"cp-1": {Role: RoleServer, Capabilities: []string{CapabilityEdge, CapabilityRegistry}},
		"db-1": {Role: RoleAgent, Capabilities: []string{CapabilityDatabase}},
		"db-2": {Role: RoleAgent, Capabilities: []string{CapabilityDatabase}},
		"app1": {Role: RoleAgent, Capabilities: []string{CapabilityApplication}},
	}
	require.Equal(t,
		[]string{CapabilityApplication, CapabilityDatabase, CapabilityRegistry, CapabilityEdge},
		UnionCapabilities(nodes))
	require.Nil(t, UnionCapabilities(nil))
}
