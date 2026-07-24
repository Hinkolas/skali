package clusterstate

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/layout"
)

func TestOperationOrdersServersThenAgentsAndResumes(t *testing.T) {
	now := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	state, err := NewSeedState("test", Node{
		ID: "seed", InstallationID: "seed-install", Name: "seed",
		Role: layout.RoleServer, Capabilities: append([]string(nil), layout.Capabilities...),
	}, now)
	require.NoError(t, err)
	server := Node{
		ID: "server-2", InstallationID: "server-install", Name: "server-2",
		Role: layout.RoleServer, Capabilities: []string{layout.CapabilityDatabase},
		Phase: NodePhaseAwaitingApply,
	}
	agent := Node{
		ID: "agent-1", InstallationID: "agent-install", Name: "agent-1",
		Role: layout.RoleAgent, Capabilities: []string{layout.CapabilityApplication},
		Phase: NodePhaseAwaitingApply,
	}
	state.Nodes[server.ID] = server
	state.Nodes[agent.ID] = agent
	_, err = state.EditCandidate(now.Add(time.Second),
		func(nodes map[string]RevisionNode, _ *PlatformState) error {
			nodes[server.ID] = revisionNode(server)
			nodes[agent.ID] = revisionNode(agent)
			return nil
		})
	require.NoError(t, err)

	_, operation, err := FreezeCandidate(state, false, now.Add(2*time.Second))
	require.NoError(t, err)
	runnable, err := RunnableNodeActions(state)
	require.NoError(t, err)
	require.Equal(t, []string{server.ID}, stepKeys(runnable))

	require.NoError(t, StartNodeAction(state, server.ID, "server-attempt", now))
	require.NoError(t, CompleteNodeAction(state, server.ID, "server-attempt", true, "", now))
	runnable, err = RunnableNodeActions(state)
	require.NoError(t, err)
	require.Equal(t, []string{agent.ID}, stepKeys(runnable))

	require.NoError(t, StartNodeAction(state, agent.ID, "agent-attempt", now))
	require.NoError(t, CompleteNodeAction(state, agent.ID, "agent-attempt", false, "temporary", now))
	require.Equal(t, OperationFailed, state.Operations[operation.ID].Phase)
	runnable, err = RunnableNodeActions(state)
	require.NoError(t, err)
	require.Empty(t, runnable, "a failed operation waits for an explicit resume")

	_, resumed, err := FreezeCandidate(state, false, now.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, operation.ID, resumed.ID)
	require.Equal(t, OperationPending, state.Operations[operation.ID].Phase)
	runnable, err = RunnableNodeActions(state)
	require.NoError(t, err)
	require.Equal(t, []string{agent.ID}, stepKeys(runnable))
	require.NoError(t, StartNodeAction(state, agent.ID, "agent-retry", now.Add(2*time.Minute)))
	require.Equal(t, "agent-retry",
		state.Operations[operation.ID].NodeSteps[agent.ID].AttemptID)
	require.NoError(t, CompleteNodeAction(state, agent.ID, "agent-retry", true, "",
		now.Add(2*time.Minute)))
}

func TestRemovalSeparatesClusterPreparationFromLocalCleanup(t *testing.T) {
	now := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	state, err := NewSeedState("test", Node{
		ID: "seed", InstallationID: "seed-install", Name: "seed",
		Role: layout.RoleServer, Capabilities: append([]string(nil), layout.Capabilities...),
	}, now)
	require.NoError(t, err)
	agent := Node{
		ID: "agent-1", InstallationID: "agent-install", Name: "agent-1",
		Role: layout.RoleAgent, Capabilities: []string{layout.CapabilityApplication},
		Phase: NodePhaseActive,
	}
	state.Nodes[agent.ID] = agent
	converged := state.Revisions[state.ConvergedRevision]
	converged.Nodes[agent.ID] = revisionNode(agent)
	state.Revisions[converged.ID] = converged
	_, err = state.EditCandidate(now.Add(time.Second),
		func(nodes map[string]RevisionNode, _ *PlatformState) error {
			delete(nodes, agent.ID)
			return nil
		})
	require.NoError(t, err)
	_, operation, err := FreezeCandidate(state, false, now.Add(2*time.Second))
	require.NoError(t, err)

	require.NoError(t, MarkTopologyActivated(state, now.Add(3*time.Second)))
	require.NoError(t, AdvancePlatform(state, true, "", now.Add(4*time.Second)))
	require.NoError(t, MarkNodeRemovalPrepared(state, agent.ID, now.Add(5*time.Second)))
	step := state.Operations[operation.ID].NodeSteps[agent.ID]
	require.True(t, step.ClusterPrepared)
	require.Equal(t, StepPending, step.Phase)
	require.Equal(t, NodePhaseAwaitingCleanup, state.Nodes[agent.ID].Phase)

	require.NoError(t, StartNodeAction(state, agent.ID, "remove-attempt", now.Add(6*time.Second)))
	require.NoError(t, CompleteNodeAction(state, agent.ID, "remove-attempt",
		true, "", now.Add(7*time.Second)))
	require.Equal(t, NodePhaseAwaitingCleanup, state.Nodes[agent.ID].Phase)
	require.NoError(t, CompleteNodeCleanup(state, agent.ID, now.Add(8*time.Second)))
	require.Equal(t, NodePhaseRemoved, state.Nodes[agent.ID].Phase)
}

func TestReplaceCandidateLayoutUsesOnlyEnrolledIdentity(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	state, err := NewSeedState("test", Node{
		ID: "seed", InstallationID: "seed-install", Name: "seed",
		Role: layout.RoleServer, Capabilities: append([]string(nil), layout.Capabilities...),
	}, now)
	require.NoError(t, err)
	agent := Node{
		ID: "agent-1", InstallationID: "agent-install", Name: "agent-1",
		Role: layout.RoleAgent, Capabilities: []string{layout.CapabilityApplication},
		Phase: NodePhaseAwaitingApply,
	}
	state.Nodes[agent.ID] = agent
	_, err = state.EditCandidate(now, func(nodes map[string]RevisionNode,
		_ *PlatformState) error {
		nodes[agent.ID] = revisionNode(agent)
		return nil
	})
	require.NoError(t, err)

	revision, err := state.ReplaceCandidateLayout(layout.Layout{
		Name: "test",
		Nodes: map[string]layout.Node{
			"seed": {
				Role: layout.RoleServer, Capabilities: append([]string(nil), layout.Capabilities...),
			},
			"agent-1": {
				Role:         layout.RoleAgent,
				Capabilities: []string{layout.CapabilityDatabase},
			},
		},
	}, now.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, []string{layout.CapabilityDatabase},
		revision.Nodes[agent.ID].Capabilities)

	_, err = state.ReplaceCandidateLayout(layout.Layout{
		Name: "test",
		Nodes: map[string]layout.Node{
			"unknown": {
				Role:         layout.RoleAgent,
				Capabilities: []string{layout.CapabilityApplication},
			},
		},
	}, now.Add(2*time.Second))
	require.ErrorContains(t, err, "has not enrolled")
}

func stepKeys(steps map[string]NodeStep) []string {
	result := make([]string, 0, len(steps))
	for key := range steps {
		result = append(result, key)
	}
	return result
}
