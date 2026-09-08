package clusterstate

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/utils"
)

type ActionKind string

const (
	ActionAddServer          ActionKind = "add-server"
	ActionAddAgent           ActionKind = "add-agent"
	ActionChangeCapabilities ActionKind = "change-capabilities"
	ActionUpgradeNode        ActionKind = "upgrade-node"
	ActionMoveWorkloads      ActionKind = "move-required-workloads"
	ActionReconcilePlatform  ActionKind = "reconcile-platform"
	ActionRemoveAgent        ActionKind = "remove-agent"
	ActionRemoveServer       ActionKind = "remove-server"
	ActionRebalanceWorkloads ActionKind = "rebalance-workloads"
)

type Action struct {
	Kind        ActionKind `json:"kind"`
	NodeID      string     `json:"nodeId,omitempty"`
	NodeName    string     `json:"nodeName,omitempty"`
	Role        string     `json:"role,omitempty"`
	From        []string   `json:"from,omitempty"`
	To          []string   `json:"to,omitempty"`
	Destructive bool       `json:"destructive,omitempty"`
	Detail      string     `json:"detail,omitempty"`
}

type Plan struct {
	FromRevision       string      `json:"fromRevision"`
	TargetRevision     string      `json:"targetRevision"`
	Actions            []Action    `json:"actions"`
	Warnings           []string    `json:"warnings,omitempty"`
	Destructive        bool        `json:"destructive,omitempty"`
	DatabaseTierFrom   layout.Tier `json:"databaseTierFrom"`
	DatabaseTierTo     layout.Tier `json:"databaseTierTo"`
	RebalanceWorkloads bool        `json:"rebalanceWorkloads,omitempty"`
}

func BuildPlan(state *State, from, target Revision, rebalanceWorkloads bool) (Plan, error) {
	if err := validateRevision(target.Nodes, target.Platform); err != nil {
		return Plan{}, err
	}
	plan := Plan{
		FromRevision: from.ID, TargetRevision: target.ID,
		DatabaseTierFrom: tier(from.Nodes), DatabaseTierTo: tier(target.Nodes),
		RebalanceWorkloads: rebalanceWorkloads,
	}

	// Additions are intentionally server-first; the reconciler executes
	// servers serially and agents with bounded parallelism.
	for _, node := range SortedRevisionNodes(target.Nodes) {
		old, exists := from.Nodes[node.ID]
		if !exists {
			kind := ActionAddAgent
			if node.Role == layout.RoleServer {
				kind = ActionAddServer
			}
			plan.Actions = append(plan.Actions, Action{
				Kind: kind, NodeID: node.ID, NodeName: node.Name, Role: node.Role,
				To: append([]string(nil), node.Capabilities...),
			})
			continue
		}
		if old.Role != node.Role {
			return Plan{}, fmt.Errorf("node %s cannot change role from %s to %s; remove and re-enroll it",
				node.Name, old.Role, node.Role)
		}
		if !utils.SameStrings(old.Capabilities, node.Capabilities) {
			plan.Actions = append(plan.Actions, Action{
				Kind: ActionChangeCapabilities, NodeID: node.ID, NodeName: node.Name,
				From: append([]string(nil), old.Capabilities...),
				To:   append([]string(nil), node.Capabilities...),
			})
			if removesAny(old.Capabilities, node.Capabilities) {
				plan.Actions = append(plan.Actions, Action{
					Kind: ActionMoveWorkloads, NodeID: node.ID, NodeName: node.Name,
					Detail: "move workloads that no longer match the node's capabilities",
				})
			}
		}
	}

	// A version change upgrades every retained node (servers first, matching
	// the serial execution order) and then moves the bundle; the platform
	// reconcile carries the new images.
	versionChanged := target.Platform.Version != "" && target.Platform.Version != from.Platform.Version
	if versionChanged {
		for _, node := range SortedRevisionNodes(target.Nodes) {
			if _, exists := from.Nodes[node.ID]; !exists {
				// A node added in the same revision installs at the new
				// version; it needs no separate upgrade.
				continue
			}
			plan.Actions = append(plan.Actions, Action{
				Kind: ActionUpgradeNode, NodeID: node.ID, NodeName: node.Name, Role: node.Role,
				From: []string{from.Platform.Version}, To: []string{target.Platform.Version},
				Detail: "hostd and k3s to " + target.Platform.Version,
			})
		}
	}

	if target.Platform.Enabled && (!from.Platform.Enabled || versionChanged ||
		plan.DatabaseTierFrom != plan.DatabaseTierTo || topologyChanged(plan.Actions)) {
		detail := fmt.Sprintf("reconcile platform once at database tier %s", plan.DatabaseTierTo)
		if versionChanged {
			detail = "move the platform bundle to " + target.Platform.Version
		}
		plan.Actions = append(plan.Actions, Action{Kind: ActionReconcilePlatform, Detail: detail})
	}

	// Removals are agent-first in display but the reconciler may impose
	// additional data and quorum sequencing.
	var removals []Action
	for _, node := range SortedRevisionNodes(from.Nodes) {
		if _, exists := target.Nodes[node.ID]; exists {
			continue
		}
		kind := ActionRemoveAgent
		if node.Role == layout.RoleServer {
			kind = ActionRemoveServer
		}
		removals = append(removals, Action{
			Kind: kind, NodeID: node.ID, NodeName: node.Name, Role: node.Role,
			From: append([]string(nil), node.Capabilities...), Destructive: true,
		})
		plan.Destructive = true
	}
	sort.SliceStable(removals, func(i, j int) bool {
		return removals[i].Kind == ActionRemoveAgent && removals[j].Kind == ActionRemoveServer
	})
	plan.Actions = append(plan.Actions, removals...)

	if rebalanceWorkloads {
		plan.Actions = append(plan.Actions, Action{
			Kind: ActionRebalanceWorkloads, Detail: "roll eligible managed Deployments one at a time",
			Destructive: true,
		})
		plan.Destructive = true
	}

	// Reachability is observed state, not a network side effect: planning
	// stays pure and deterministic while still explaining which affected
	// agents have not recently checked in.
	affected := make(map[string]bool)
	for _, action := range plan.Actions {
		if action.NodeID == "" || affected[action.NodeID] {
			continue
		}
		affected[action.NodeID] = true
		node, ok := state.Nodes[action.NodeID]
		if !ok {
			continue
		}
		switch {
		case node.Phase == NodePhaseFailed:
			plan.Warnings = append(plan.Warnings,
				fmt.Sprintf("node %s reports a failed agent state: %s",
					node.Name, strings.TrimSpace(node.LastError)))
		case node.LastSeen.IsZero():
			plan.Warnings = append(plan.Warnings,
				fmt.Sprintf("node %s has not completed its first coordinator heartbeat", node.Name))
		case state.UpdatedAt.Sub(node.LastSeen) > 30*time.Second:
			plan.Warnings = append(plan.Warnings,
				fmt.Sprintf("node %s heartbeat is stale by %s", node.Name,
					state.UpdatedAt.Sub(node.LastSeen).Round(time.Second)))
		}
	}

	servers := 0
	for _, node := range target.Nodes {
		if node.Role == layout.RoleServer {
			servers++
		}
	}
	if servers > 1 && servers%2 == 0 {
		plan.Warnings = append(plan.Warnings,
			fmt.Sprintf("target has %d servers; an odd count gives better etcd failure tolerance", servers))
	}
	if target.Platform.RegistryNode != "" {
		if _, ok := target.Nodes[target.Platform.RegistryNode]; !ok {
			return Plan{}, errorsRegistryNode()
		}
	}
	return plan, nil
}

func errorsRegistryNode() error {
	return fmt.Errorf("the node holding the registry's local data cannot be removed; registry migration is not implemented")
}

func topologyChanged(actions []Action) bool {
	for _, action := range actions {
		switch action.Kind {
		case ActionAddServer, ActionAddAgent, ActionChangeCapabilities,
			ActionRemoveAgent, ActionRemoveServer:
			return true
		}
	}
	return false
}

func tier(nodes map[string]RevisionNode) layout.Tier {
	count := 0
	for _, node := range nodes {
		if slices.Contains(node.Capabilities, layout.CapabilityDatabase) {
			count++
		}
	}
	return layout.DeriveTier(count)
}

func removesAny(from, to []string) bool {
	for _, value := range from {
		if !slices.Contains(to, value) {
			return true
		}
	}
	return false
}

func (p Plan) Empty() bool { return len(p.Actions) == 0 }

func (p Plan) String() string {
	var builder strings.Builder
	for _, action := range p.Actions {
		fmt.Fprintf(&builder, "%-26s", action.Kind)
		if action.NodeName != "" {
			fmt.Fprintf(&builder, " %s", action.NodeName)
		}
		if len(action.From) > 0 || len(action.To) > 0 {
			fmt.Fprintf(&builder, " %s -> %s", strings.Join(action.From, ","),
				strings.Join(action.To, ","))
		}
		if action.Detail != "" {
			fmt.Fprintf(&builder, "  %s", action.Detail)
		}
		builder.WriteByte('\n')
	}
	return builder.String()
}
