package installer

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/layout"
)

func labeledNode(name string, server bool, capabilities ...string) corev1.Node {
	labels := layout.CapabilityLabels(capabilities)
	labels[layout.ClusterLabel] = "production"
	if server {
		labels[layout.ControlPlaneLabel] = "true"
	}
	return corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

func TestLayoutFromNodes(t *testing.T) {
	t.Parallel()
	nodes := []corev1.Node{
		labeledNode("cp-1", true, layout.Capabilities...),
	}
	rebuilt := LayoutFromNodes(nodes, "production")
	require.Equal(t, "production", rebuilt.Name)
	require.Len(t, rebuilt.Nodes, 1)
	require.Equal(t, layout.RoleServer, rebuilt.Nodes["cp-1"].Role)
	require.Equal(t, layout.Capabilities, rebuilt.Nodes["cp-1"].Capabilities)

	topology := rebuilt.Topology()
	require.Equal(t, 1, topology.Servers)
	require.Equal(t, layout.TierSingle, topology.DatabaseTier)
}

func TestAssertLayout(t *testing.T) {
	t.Parallel()
	live := LayoutFromNodes([]corev1.Node{
		labeledNode("cp-1", true, layout.CapabilityEdge, layout.CapabilityRegistry),
		labeledNode("db-1", false, layout.CapabilityDatabase),
	}, "production")

	matching := layout.Layout{
		Version: layout.CurrentVersion,
		Name:    "production",
		Nodes: map[string]layout.Node{
			"cp-1": {Role: layout.RoleServer, Capabilities: []string{layout.CapabilityRegistry, layout.CapabilityEdge}},
			"db-1": {Role: layout.RoleAgent, Capabilities: []string{layout.CapabilityDatabase}},
		},
	}
	require.NoError(t, assertLayout(matching, live))

	// Every difference is listed, not just the first.
	mismatched := layout.Layout{
		Version: layout.CurrentVersion,
		Name:    "production",
		Nodes: map[string]layout.Node{
			"cp-1": {Role: layout.RoleAgent, Capabilities: []string{layout.CapabilityEdge}},
			"db-2": {Role: layout.RoleAgent, Capabilities: []string{layout.CapabilityDatabase}},
		},
	}
	err := assertLayout(mismatched, live)
	require.Error(t, err)
	message := err.Error()
	require.Contains(t, message, "cp-1: expected role agent, found server")
	require.Contains(t, message, "cp-1: expected capabilities edge, found registry, edge")
	require.Contains(t, message, "db-2 is expected but has not joined")
	require.Contains(t, message, "db-1 has joined but is not in the layout")
}

func TestPrintLayoutAndTopology(t *testing.T) {
	t.Parallel()
	live := LayoutFromNodes([]corev1.Node{
		labeledNode("cp-1", true, layout.Capabilities...),
	}, "production")
	var out strings.Builder
	printLayout(&out, live)
	printTopology(&out, live.Topology(), firstCapableNode(live, layout.CapabilityRegistry))
	rendered := out.String()
	require.Contains(t, rendered, "cluster layout")
	require.Contains(t, rendered, "NODE")
	require.Contains(t, rendered, "cp-1  server  application, database, object-storage, registry, edge")
	require.Contains(t, rendered, "database availability tier  single (1 database node)")
	require.Contains(t, rendered, "registry placement          cp-1 (installer-owned volume)")
}
