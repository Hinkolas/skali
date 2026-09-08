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

// upgradeReadyState is a converged, initialized two-server one-agent
// cluster whose nodes all polled recently, the shape a console update
// starts from.
func upgradeReadyState(t *testing.T, now time.Time) *State {
	t.Helper()
	state, err := NewSeedState("test", Node{
		ID: "seed", InstallationID: "seed-install", Name: "seed",
		Role: layout.RoleServer, Capabilities: append([]string(nil), layout.Capabilities...),
	}, now)
	require.NoError(t, err)
	server := Node{
		ID: "server-2", InstallationID: "server-install", Name: "server-2",
		Role: layout.RoleServer, Capabilities: []string{layout.CapabilityDatabase},
		Phase: NodePhaseActive,
	}
	agent := Node{
		ID: "agent-1", InstallationID: "agent-install", Name: "agent-1",
		Role: layout.RoleAgent, Capabilities: []string{layout.CapabilityApplication},
		Phase: NodePhaseActive,
	}
	state.Nodes[server.ID] = server
	state.Nodes[agent.ID] = agent
	_, err = state.EditCandidate(now, func(nodes map[string]RevisionNode, platform *PlatformState) error {
		nodes[server.ID] = revisionNode(server)
		nodes[agent.ID] = revisionNode(agent)
		platform.Enabled = true
		platform.RegistryNode = "seed"
		platform.Version = "v0.1.0"
		return nil
	})
	require.NoError(t, err)
	// Converge the candidate directly: the topology is already in place.
	state.ConvergedRevision = state.CandidateRevision
	state.Platform = state.Revisions[state.ConvergedRevision].Platform
	for id, node := range state.Nodes {
		node.LastSeen = now
		state.Nodes[id] = node
	}
	return state
}

func TestRequestPlatformVersionUpgradesNodesSeriallyThenPlatform(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	state := upgradeReadyState(t, now)

	plan, operation, err := RequestPlatformVersion(state, "v0.2.0", now)
	require.NoError(t, err)
	require.NotEmpty(t, operation.ID)
	kinds := make([]ActionKind, 0, len(plan.Actions))
	for _, action := range plan.Actions {
		kinds = append(kinds, action.Kind)
	}
	require.Equal(t, []ActionKind{ActionUpgradeNode, ActionUpgradeNode, ActionUpgradeNode,
		ActionReconcilePlatform}, kinds)
	require.Equal(t, "v0.2.0", state.Revisions[state.TargetRevision].Platform.Version)
	require.Equal(t, "seed", state.Revisions[state.TargetRevision].Platform.RegistryNode,
		"the rest of the platform state rides along unchanged")

	// Servers first by name, strictly one at a time, then the agent.
	runnable, err := RunnableNodeActions(state)
	require.NoError(t, err)
	require.Equal(t, []string{"seed"}, stepKeys(runnable))
	require.NoError(t, StartNodeAction(state, "seed", "a1", now))
	require.Equal(t, OperationUpgrading, state.Operations[operation.ID].Phase)
	runnable, err = RunnableNodeActions(state)
	require.NoError(t, err)
	require.Equal(t, []string{"seed"}, stepKeys(runnable), "the running step is the only one exposed")
	require.NoError(t, CompleteNodeAction(state, "seed", "a1", true, "", now))
	require.Equal(t, NodePhaseActive, state.Nodes["seed"].Phase)
	runnable, err = RunnableNodeActions(state)
	require.NoError(t, err)
	require.Equal(t, []string{"server-2"}, stepKeys(runnable))
	require.NoError(t, StartNodeAction(state, "server-2", "a2", now))
	require.NoError(t, CompleteNodeAction(state, "server-2", "a2", true, "", now))
	runnable, err = RunnableNodeActions(state)
	require.NoError(t, err)
	require.Equal(t, []string{"agent-1"}, stepKeys(runnable))

	// The platform waits for the last upgrade.
	require.Error(t, AdvancePlatform(state, true, "", now))
	require.NoError(t, StartNodeAction(state, "agent-1", "a3", now))
	require.NoError(t, CompleteNodeAction(state, "agent-1", "a3", true, "", now))
	runnable, err = RunnableNodeActions(state)
	require.NoError(t, err)
	require.Empty(t, runnable)
	require.NoError(t, MarkTopologyActivated(state, now))
	require.NoError(t, AdvancePlatform(state, true, "", now))
	require.Equal(t, OperationVerifying, state.Operations[operation.ID].Phase)
	require.NoError(t, CompleteOperation(state, now))
	require.Equal(t, "v0.2.0", state.Platform.Version)
	require.Empty(t, state.CurrentOperation)

	// Repeating the same version is refused as not newer.
	_, _, err = RequestPlatformVersion(state, "v0.2.0", now)
	require.ErrorContains(t, err, "not newer")
}

func TestRequestPlatformVersionRefusals(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)

	state := upgradeReadyState(t, now)
	_, _, err := RequestPlatformVersion(state, "v0.2.0-3-gabc1234", now)
	require.ErrorContains(t, err, "not a tagged release")

	state = upgradeReadyState(t, now)
	_, _, err = RequestPlatformVersion(state, "v0.0.9", now)
	require.ErrorContains(t, err, "not newer")

	state = upgradeReadyState(t, now)
	node := state.Nodes["agent-1"]
	node.LastSeen = now.Add(-HeartbeatWindow - time.Second)
	state.Nodes["agent-1"] = node
	_, _, err = RequestPlatformVersion(state, "v0.2.0", now)
	require.ErrorContains(t, err, "agent-1 has not reported")

	state = upgradeReadyState(t, now)
	_, err = state.EditCandidate(now, func(nodes map[string]RevisionNode, _ *PlatformState) error {
		delete(nodes, "agent-1")
		return nil
	})
	require.NoError(t, err)
	_, _, err = RequestPlatformVersion(state, "v0.2.0", now)
	require.ErrorContains(t, err, "staged but not applied")

	state = upgradeReadyState(t, now)
	_, _, err = RequestPlatformVersion(state, "v0.2.0", now)
	require.NoError(t, err)
	_, _, err = RequestPlatformVersion(state, "v0.3.0", now)
	require.ErrorIs(t, err, ErrOperationActive)

	// A failed upgrade step resumes without replaying the proven ones.
	state = upgradeReadyState(t, now)
	_, operation, err := RequestPlatformVersion(state, "v0.2.0", now)
	require.NoError(t, err)
	require.NoError(t, StartNodeAction(state, "seed", "a1", now))
	require.NoError(t, CompleteNodeAction(state, "seed", "a1", true, "", now))
	require.NoError(t, StartNodeAction(state, "server-2", "a2", now))
	require.NoError(t, CompleteNodeAction(state, "server-2", "a2", false, "download failed", now))
	require.Equal(t, OperationFailed, state.Operations[operation.ID].Phase)
	_, resumed, err := FreezeCandidate(state, false, now.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, operation.ID, resumed.ID)
	runnable, err := RunnableNodeActions(state)
	require.NoError(t, err)
	require.Equal(t, []string{"server-2"}, stepKeys(runnable))
	require.Equal(t, StepComplete, state.Operations[operation.ID].NodeSteps["seed"].Phase)
}
