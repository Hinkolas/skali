package clusterstate

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/Hinkolas/skali/internal/version"
	"github.com/google/uuid"
)

const UpdateJournalKey = "updates.json"

type CoordinatorReport struct {
	Version  string    `json:"version"`
	LastSeen time.Time `json:"lastSeen"`
}

type ReleaseUpdate struct {
	Version string `json:"version"`
	K3s     string `json:"k3s,omitempty"`
}

type UpdateJournal struct {
	Operations   map[string]ReleaseUpdate     `json:"operations,omitempty"`
	Coordinators map[string]CoordinatorReport `json:"coordinators,omitempty"`
}

func decodeDocument(data map[string]string) (*State, error) {
	state, err := decodeState(data[StateKey])
	if err != nil {
		return nil, err
	}
	if raw := data[UpdateJournalKey]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &state.Updates); err != nil {
			return nil, fmt.Errorf("decode update journal: %w", err)
		}
	}
	return state, nil
}

func IsReleaseOperation(state *State, op Operation) bool {
	if _, ok := state.Updates.Operations[op.ID]; ok {
		return true
	}
	from, target := state.Revisions[op.FromRevision], state.Revisions[op.TargetRevision]
	return target.Platform.Version != "" && target.Platform.Version != from.Platform.Version
}

// RequestRelease creates an explicitly journaled update, including a repair
// whose release already matches the converged revision. It never edits topology.
func RequestRelease(state *State, target string, now time.Time) (Operation, error) {
	if !version.IsRelease(target) {
		return Operation{}, fmt.Errorf("%q is not a tagged release", target)
	}
	if err := Updatable(state, now); err != nil {
		return Operation{}, err
	}
	from, err := state.Converged()
	if err != nil {
		return Operation{}, err
	}
	if from.Platform.Version != "" && version.Older(target, from.Platform.Version) {
		return Operation{}, fmt.Errorf("cannot downgrade cluster from %s to %s", from.Platform.Version, target)
	}
	for id := range from.Nodes {
		node := state.Nodes[id]
		installedVersions := []string{node.AgentVersion}
		if from.Nodes[id].Role == "server" {
			installedVersions = append(installedVersions, state.Updates.Coordinators[id].Version)
		}
		for _, installed := range installedVersions {
			if installed != "" && (!version.IsRelease(installed) || version.Older(target, installed)) {
				return Operation{}, fmt.Errorf("node %s runs %s; cannot move it to %s", node.Name, installed, target)
			}
		}
	}
	revision, err := state.EditCandidate(now, func(_ map[string]RevisionNode, platform *PlatformState) error {
		platform.Version = target
		return nil
	})
	if err != nil {
		return Operation{}, err
	}
	op := Operation{ID: uuid.NewString(), FromRevision: from.ID, TargetRevision: revision.ID,
		Phase: OperationPending, StartedAt: now, UpdatedAt: now, NodeSteps: map[string]NodeStep{}}
	for id := range revision.Nodes {
		op.NodeSteps[id] = NodeStep{Action: NodeActionUpgrade, Phase: StepPending, UpdatedAt: now}
	}
	if state.Operations == nil {
		state.Operations = map[string]Operation{}
	}
	if state.Updates.Operations == nil {
		state.Updates.Operations = map[string]ReleaseUpdate{}
	}
	state.Operations[op.ID] = op
	state.Updates.Operations[op.ID] = ReleaseUpdate{Version: target}
	state.CurrentOperation, state.TargetRevision = op.ID, revision.ID
	return op, nil
}

func ResumeRelease(state *State, now time.Time) (Operation, error) {
	op, ok := state.Operations[state.CurrentOperation]
	if !ok || !IsReleaseOperation(state, op) || op.Phase != OperationFailed {
		return Operation{}, ErrOperationActive
	}
	target := state.Revisions[op.TargetRevision]
	for id, desired := range target.Nodes {
		node := state.Nodes[id]
		if node.LastSeen.IsZero() || now.Sub(node.LastSeen) > HeartbeatWindow {
			return Operation{}, fmt.Errorf("node %s has not reported recently", desired.Name)
		}
		installedVersions := []string{node.AgentVersion}
		if desired.Role == "server" {
			installedVersions = append(installedVersions, state.Updates.Coordinators[id].Version)
		}
		for _, installed := range installedVersions {
			if installed != "" && (!version.IsRelease(installed) || version.Older(target.Platform.Version, installed)) {
				return Operation{}, fmt.Errorf("node %s runs %s; cannot resume update to %s", desired.Name, installed, target.Platform.Version)
			}
		}
	}
	_, resumed, err := FreezeCandidate(state, op.RebalanceWorkloads, now)
	if err != nil {
		return Operation{}, err
	}
	// Preserve proven work, but a completed step whose services have since
	// drifted is no longer proof. Revisit only those nodes on explicit retry.
	for id, step := range resumed.NodeSteps {
		if step.Phase != StepComplete || step.Action != NodeActionUpgrade {
			continue
		}
		node, coordinator := state.Nodes[id], state.Updates.Coordinators[id]
		pin := state.Updates.Operations[op.ID].K3s
		drifted := node.AgentVersion != target.Platform.Version || pin != "" && node.K3sVersion != pin
		if target.Nodes[id].Role == "server" {
			drifted = drifted || coordinator.Version != target.Platform.Version || coordinator.LastSeen.IsZero() || now.Sub(coordinator.LastSeen) > HeartbeatWindow
		}
		if drifted {
			step.Phase, step.AttemptID, step.LastError, step.UpdatedAt = StepPending, "", "", now
			resumed.NodeSteps[id] = step
		}
	}
	state.Operations[resumed.ID] = resumed
	return resumed, nil
}
