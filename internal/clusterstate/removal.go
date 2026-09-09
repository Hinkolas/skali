package clusterstate

import (
	"errors"
	"fmt"
	"time"
)

// StageNodeRemoval must run inside Store.Update: apply and cancellation then
// contend on the same CAS authority. A node in an active target is never treated
// as an unapplied enrollment, even if it has not started k3s yet.
func StageNodeRemoval(state *State, name string, now time.Time) (bool, error) {
	cancelled := false
	candidate, err := state.Candidate()
	if err != nil {
		return false, err
	}
	if _, exists := FindNodeByName(candidate.Nodes, name); !exists {
		historicalCancellation := false
		for _, node := range SortedNodes(state.Nodes) {
			if node.Name != name {
				continue
			}
			if node.Phase == NodePhaseCancelled {
				historicalCancellation = true
				continue
			}
			if node.Phase == NodePhaseRemoved {
				continue
			}
			// alpha.2 left never-applied cancellations awaiting unreachable agents.
			if node.LastAction == "cancel-enrollment" && node.Phase == NodePhaseUninstalling {
				activeID := state.ConvergedRevision
				if state.TargetRevision != "" {
					activeID = state.TargetRevision
				}
				if _, active := state.Revisions[activeID].Nodes[node.ID]; !active {
					node.Phase = NodePhaseCancelled
					node.UpdatedAt = now.UTC().Truncate(time.Second)
					state.Nodes[node.ID] = node
					return true, nil
				}
			}
			return false, fmt.Errorf("node %q is already absent from the candidate; review skali cluster plan for its pending removal", name)
		}
		if historicalCancellation {
			return true, nil
		}
	}
	_, err = state.EditCandidate(now, func(nodes map[string]RevisionNode, platform *PlatformState) error {
		node, ok := FindNodeByName(nodes, name)
		if !ok {
			return fmt.Errorf("node %q is not in the candidate", name)
		}
		if platform.RegistryNode == node.ID {
			return errors.New("the node holds the registry's local data; registry migration is not implemented")
		}
		delete(nodes, node.ID)
		activeID := state.ConvergedRevision
		if state.TargetRevision != "" {
			activeID = state.TargetRevision
		}
		if _, applied := state.Revisions[activeID].Nodes[node.ID]; !applied {
			observed := state.Nodes[node.ID]
			observed.Phase = NodePhaseCancelled
			observed.LastAction = "cancel-enrollment"
			observed.LastError = ""
			observed.UpdatedAt = now.UTC().Truncate(time.Second)
			state.Nodes[node.ID] = observed
			cancelled = true
		}
		return nil
	})
	return cancelled, err
}
