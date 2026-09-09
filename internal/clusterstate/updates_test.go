package clusterstate

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Hinkolas/skali/internal/layout"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func updateState(t *testing.T, now time.Time) *State {
	t.Helper()
	state, err := NewSeedState("test", Node{ID: "seed", InstallationID: "install", Name: "seed", Role: "server", Capabilities: layout.Capabilities, AgentVersion: "v0.1.0-alpha.4", K3sVersion: "v1.36.3+k3s1", LastSeen: now}, now)
	require.NoError(t, err)
	_, err = state.EditCandidate(now, func(nodes map[string]RevisionNode, platform *PlatformState) error {
		platform.Enabled, platform.Version, platform.RegistryNode = true, "v0.1.0-alpha.4", "seed"
		for i := 1; i < 11; i++ {
			id := fmt.Sprintf("node-%02d", i)
			role := "agent"
			if i < 3 {
				role = "server"
			}
			nodes[id] = RevisionNode{ID: id, Name: id, Role: role, Capabilities: layout.Capabilities}
			state.Nodes[id] = Node{ID: id, Name: id, Role: role, Phase: NodePhaseActive, AgentVersion: "v0.1.0-alpha.4", LastSeen: now}
		}
		return nil
	})
	require.NoError(t, err)
	state.ConvergedRevision = state.CandidateRevision
	state.Updates.Coordinators = map[string]CoordinatorReport{}
	for id, node := range state.Nodes {
		if node.Role == "server" {
			state.Updates.Coordinators[id] = CoordinatorReport{Version: "v0.1.0-alpha.4", LastSeen: now}
		}
	}
	return state
}

func TestReleaseRepairSerializesAndResumesElevenNodes(t *testing.T) {
	now := time.Now()
	state := updateState(t, now)
	op, err := RequestRelease(state, "v0.1.0-alpha.4", now)
	require.NoError(t, err)
	require.Len(t, op.NodeSteps, 11)
	require.True(t, IsReleaseOperation(state, op))
	seenAgent := false
	for i := range 11 {
		actions, err := RunnableNodeActions(state)
		require.NoError(t, err)
		require.Len(t, actions, 1)
		for id := range actions {
			if state.Nodes[id].Role == "agent" {
				seenAgent = true
			} else {
				require.False(t, seenAgent)
			}
			attempt := fmt.Sprintf("attempt-%d", i)
			require.NoError(t, StartNodeAction(state, id, attempt, now))
			if i == 5 {
				require.NoError(t, CompleteNodeAction(state, id, attempt, false, "download failed", now))
				resumed, err := ResumeRelease(state, now)
				require.NoError(t, err)
				require.Equal(t, op.ID, resumed.ID)
				attempt += "-retry"
				require.NoError(t, StartNodeAction(state, id, attempt, now))
			}
			require.NoError(t, CompleteNodeAction(state, id, attempt, true, "", now))
		}
		// A restarted controller reconstructs both documents between steps.
		raw, err := json.Marshal(state)
		require.NoError(t, err)
		journal, err := json.Marshal(state.Updates)
		require.NoError(t, err)
		state, err = decodeDocument(map[string]string{StateKey: string(raw), UpdateJournalKey: string(journal)})
		require.NoError(t, err)
	}
	actions, err := RunnableNodeActions(state)
	require.NoError(t, err)
	require.Empty(t, actions)
}

func TestReleaseRetryWaitsForDisconnectedNodeAndRejectsNewerHost(t *testing.T) {
	now := time.Now()
	state := updateState(t, now)
	op, err := RequestRelease(state, "v0.1.0-alpha.5", now)
	require.NoError(t, err)
	require.NoError(t, FailOperation(state, "node disconnected", now))
	node := state.Nodes["node-04"]
	node.LastSeen = now.Add(-3 * time.Minute)
	state.Nodes[node.ID] = node
	_, err = ResumeRelease(state, now)
	require.ErrorContains(t, err, "not reported recently")
	node.LastSeen, node.AgentVersion = now, "v0.1.0-alpha.6"
	state.Nodes[node.ID] = node
	_, err = ResumeRelease(state, now)
	require.ErrorContains(t, err, "cannot resume")
	node.AgentVersion = "v0.1.0-alpha.4"
	state.Nodes[node.ID] = node
	resumed, err := ResumeRelease(state, now)
	require.NoError(t, err)
	require.Equal(t, op.ID, resumed.ID)
}

func TestReleaseRetryRevisitsOnlyCompletedNodesThatDrifted(t *testing.T) {
	now := time.Now()
	state := updateState(t, now)
	op, err := RequestRelease(state, "v0.1.0-alpha.4", now)
	require.NoError(t, err)
	for _, id := range []string{"seed", "node-03"} {
		require.NoError(t, StartNodeAction(state, id, id+"-attempt", now))
		require.NoError(t, CompleteNodeAction(state, id, id+"-attempt", true, "", now))
	}
	require.NoError(t, FailOperation(state, "verification failed", now))
	node := state.Nodes["node-03"]
	node.AgentVersion = "v0.1.0-alpha.3"
	state.Nodes[node.ID] = node
	resumed, err := ResumeRelease(state, now)
	require.NoError(t, err)
	require.Equal(t, op.ID, resumed.ID)
	require.Equal(t, StepComplete, resumed.NodeSteps["seed"].Phase)
	require.Equal(t, StepPending, resumed.NodeSteps[node.ID].Phase)
	require.Empty(t, resumed.NodeSteps[node.ID].AttemptID)
}

func TestAlpha4WriterPreservesNewUpdateJournal(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	store := &Store{Client: fake.NewSimpleClientset()}
	_, err := store.Bootstrap(ctx, updateState(t, now))
	require.NoError(t, err)
	state, err := store.Update(ctx, func(s *State) error {
		_, err := RequestRelease(s, "v0.1.0-alpha.4", now)
		s.Updates.Coordinators = map[string]CoordinatorReport{"seed": {Version: "v0.1.0-alpha.4", LastSeen: now}}
		return err
	})
	require.NoError(t, err)
	// Alpha.4 decodes/re-encodes state.json but preserves other ConfigMap
	// data entries. Simulate that exact write while the operation is active.
	cm, err := store.Client.CoreV1().ConfigMaps(Namespace).Get(ctx, StateName, metav1.GetOptions{})
	require.NoError(t, err)
	old, err := decodeState(cm.Data[StateKey])
	require.NoError(t, err)
	old.Nodes["seed"] = state.Nodes["seed"]
	raw, err := json.Marshal(old)
	require.NoError(t, err)
	cm.Data[StateKey] = string(raw)
	_, err = store.Client.CoreV1().ConfigMaps(Namespace).Update(ctx, cm, metav1.UpdateOptions{})
	require.NoError(t, err)
	loaded, err := store.Load(ctx)
	require.NoError(t, err)
	require.Equal(t, state.Updates.Operations, loaded.Updates.Operations)
	require.Equal(t, "v0.1.0-alpha.4", loaded.Updates.Coordinators["seed"].Version)
	require.Len(t, loaded.Operations[loaded.CurrentOperation].NodeSteps, 11)
}
