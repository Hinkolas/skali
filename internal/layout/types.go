// Package layout defines and parses the cluster-layout document consumed by
// skali cluster. The layout records which hosts form an installation, their
// K3s roles, and their designated capabilities; `skali cluster init` derives
// the data-service topology from it.
package layout

const CurrentVersion = "1"

// Node capabilities. Placement policy consumes these; they are orthogonal to
// the K3s server/agent role.
const (
	CapabilityApplication   = "application"
	CapabilityDatabase      = "database"
	CapabilityObjectStorage = "object-storage"
	CapabilityRegistry      = "registry"
	CapabilityEdge          = "edge"
)

// Capabilities lists every known capability in display order.
var Capabilities = []string{
	CapabilityApplication,
	CapabilityDatabase,
	CapabilityObjectStorage,
	CapabilityRegistry,
	CapabilityEdge,
}

// RequiredCapabilities must each be carried by at least one node: the
// bootstrap database needs a database node, artifacts need a registry node,
// routes and the API/UI need an edge node, and workloads need an application
// node. Object storage is the one optional subsystem.
var RequiredCapabilities = []string{
	CapabilityApplication,
	CapabilityDatabase,
	CapabilityRegistry,
	CapabilityEdge,
}

const (
	RoleServer = "server"
	RoleAgent  = "agent"
)

type Layout struct {
	Version string          `yaml:"version" json:"version" jsonschema:"Layout schema version. Currently 1."`
	Name    string          `yaml:"name" json:"name" jsonschema:"Stable installation name."`
	Nodes   map[string]Node `yaml:"nodes" json:"nodes" jsonschema:"Cluster hosts keyed by stable node name."`
}

type Node struct {
	Role         string   `yaml:"role" json:"role" jsonschema:"K3s role: server or agent."`
	Capabilities []string `yaml:"capabilities,omitempty" json:"capabilities,omitempty" jsonschema:"Designated workload capabilities for this node."`
}
