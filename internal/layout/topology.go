package layout

// Tier is a database availability tier derived from the number of
// database-capable nodes. The values match the manifest's database
// availability vocabulary so both stay aligned.
type Tier string

const (
	TierSingle       Tier = "single"
	TierAsynchronous Tier = "asynchronous"
	TierSynchronous  Tier = "synchronous"
)

// DeriveTier returns the best availability tier the given number of
// database-capable nodes supports. One node runs a single instance, two allow
// asynchronous replication, and three or more allow synchronous quorum
// replication. Tier changes after initialization are explicit installer
// operations, never side effects of node membership changes.
func DeriveTier(databaseNodes int) Tier {
	switch {
	case databaseNodes >= 3:
		return TierSynchronous
	case databaseNodes == 2:
		return TierAsynchronous
	default:
		return TierSingle
	}
}

// Topology summarizes a validated layout for the installer: it sizes the
// bootstrap database and the default shared pool and reports where each
// system may be placed.
type Topology struct {
	Servers      int
	Agents       int
	Capable      map[string]int
	DatabaseTier Tier
}

func (l Layout) Topology() Topology {
	topology := Topology{Capable: make(map[string]int, len(Capabilities))}
	for _, node := range l.Nodes {
		if node.Role == RoleServer {
			topology.Servers++
		} else {
			topology.Agents++
		}
		for _, capability := range node.Capabilities {
			topology.Capable[capability]++
		}
	}
	topology.DatabaseTier = DeriveTier(topology.Capable[CapabilityDatabase])
	return topology
}
