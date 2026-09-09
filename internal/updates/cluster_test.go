package updates

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/clusterstate"
	"github.com/Hinkolas/skali/internal/layout"
)

func TestProjectExcludesRetiredEnrollments(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	state := &clusterstate.State{Nodes: map[string]clusterstate.Node{}}
	for i := range 11 {
		id := fmt.Sprintf("enrollment-%02d", i)
		role := layout.RoleAgent
		if i < 3 {
			role = layout.RoleServer
		}
		state.Nodes[id] = clusterstate.Node{
			ID: id, Name: fmt.Sprintf("node-%02d", i), Role: role, Phase: clusterstate.NodePhaseActive,
			Capabilities: []string{layout.Capabilities[i%len(layout.Capabilities)]},
			LastSeen:     now,
		}
	}
	// A cancelled enrollment keeps its identity in the coordinator, and a
	// later installation can legitimately reuse its hostname.
	for _, phase := range []string{clusterstate.NodePhaseCancelled, clusterstate.NodePhaseRemoved} {
		state.Nodes[phase] = clusterstate.Node{
			ID: phase, Name: "node-03", Role: layout.RoleAgent, Phase: phase,
		}
	}

	snapshot := project(state, now)
	require.Len(t, snapshot.Nodes, 11)
	for _, node := range snapshot.Nodes {
		require.Equal(t, clusterstate.NodePhaseActive, node.Phase)
		require.Contains(t, state.Nodes, node.ID)
		require.Equal(t, state.Nodes[node.ID].Name, node.Name)
	}
}

func TestProjectOperationPreservesIdentityAcrossHostnameReuse(t *testing.T) {
	state := &clusterstate.State{Revisions: map[string]clusterstate.Revision{
		"from": {Nodes: map[string]clusterstate.RevisionNode{
			"old": {ID: "old", Name: "worker", Role: layout.RoleAgent},
		}},
		"target": {Nodes: map[string]clusterstate.RevisionNode{
			"new": {ID: "new", Name: "worker", Role: layout.RoleServer},
		}},
	}}
	operation := clusterstate.Operation{
		FromRevision: "from", TargetRevision: "target",
		NodeSteps: map[string]clusterstate.NodeStep{
			"old": {Action: clusterstate.NodeActionRemove, Phase: clusterstate.StepPending},
			"new": {Action: clusterstate.NodeActionInstall, Phase: clusterstate.StepRunning},
		},
	}
	projected := projectOperation(state, operation)
	require.Equal(t, []StepState{
		{NodeID: "new", Node: "worker", Action: clusterstate.NodeActionInstall, Phase: clusterstate.StepRunning},
		{NodeID: "old", Node: "worker", Action: clusterstate.NodeActionRemove, Phase: clusterstate.StepPending},
	}, projected.Steps)
}
