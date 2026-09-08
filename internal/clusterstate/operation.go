package clusterstate

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/version"
)

const (
	StepPending  = "pending"
	StepRunning  = "running"
	StepComplete = "complete"
	StepFailed   = "failed"
)

const (
	NodeActionInstall      = "install"
	NodeActionCapabilities = "capabilities"
	NodeActionUpgrade      = "upgrade"
	NodeActionRemove       = "remove"
)

// ErrOperationActive refuses a new request while a cluster operation is in
// flight; the caller resumes or waits for it instead.
var ErrOperationActive = errors.New("a cluster operation is already in progress")

const maxParallelAgents = 4

// FreezeCandidate makes the candidate the durable target and creates the
// resumable operation journal. Repeating it while that same target is active
// returns the existing operation.
func FreezeCandidate(state *State, rebalanceWorkloads bool, now time.Time) (Plan, Operation, error) {
	if state.CurrentOperation != "" {
		operation, ok := state.Operations[state.CurrentOperation]
		if !ok {
			return Plan{}, Operation{}, errors.New("cluster references a missing current operation")
		}
		if operation.Phase != OperationComplete {
			// An explicit repeat apply resumes the same journal. Failed
			// node steps retain their attempt history and become runnable
			// again; no proven step is replayed.
			if operation.Phase == OperationFailed {
				operation.Phase = OperationPending
				operation.LastError = ""
				operation.UpdatedAt = now.UTC().Truncate(time.Second)
				state.Operations[operation.ID] = operation
			}
			target, err := state.Target()
			if err != nil {
				return Plan{}, Operation{}, err
			}
			from, err := state.revision(operation.FromRevision, "operation source")
			if err != nil {
				return Plan{}, Operation{}, err
			}
			plan, err := BuildPlan(state, from, target, operation.RebalanceWorkloads)
			return plan, operation, err
		}
	}
	from, err := state.Converged()
	if err != nil {
		return Plan{}, Operation{}, err
	}
	target, err := state.Candidate()
	if err != nil {
		return Plan{}, Operation{}, err
	}
	plan, err := BuildPlan(state, from, target, rebalanceWorkloads)
	if err != nil {
		return Plan{}, Operation{}, err
	}
	if plan.Empty() {
		return plan, Operation{}, nil
	}
	now = now.UTC().Truncate(time.Second)
	operation := Operation{
		ID: uuid.NewString(), FromRevision: from.ID, TargetRevision: target.ID,
		Phase: OperationPending, RebalanceWorkloads: rebalanceWorkloads,
		NodeSteps: make(map[string]NodeStep), StartedAt: now, UpdatedAt: now,
	}
	for _, action := range plan.Actions {
		var stepAction string
		switch action.Kind {
		case ActionAddServer, ActionAddAgent:
			stepAction = NodeActionInstall
		case ActionChangeCapabilities:
			stepAction = NodeActionCapabilities
		case ActionUpgradeNode:
			stepAction = NodeActionUpgrade
		case ActionRemoveServer, ActionRemoveAgent:
			stepAction = NodeActionRemove
		default:
			continue
		}
		// One node may have a capability change and later removal only in
		// malformed revisions; desired-node diffing normally makes them
		// mutually exclusive.
		operation.NodeSteps[action.NodeID] = NodeStep{
			Action: stepAction, Phase: StepPending, UpdatedAt: now,
		}
	}
	if state.Operations == nil {
		state.Operations = make(map[string]Operation)
	}
	state.Operations[operation.ID] = operation
	state.TargetRevision = target.ID
	state.CurrentOperation = operation.ID
	state.UpdatedAt = now
	return plan, operation, nil
}

// RunnableNodeActions returns the node steps the leader may expose now.
// Servers install serially, agents install only after every server addition,
// capability changes follow additions, and removals run last.
func RunnableNodeActions(state *State) (map[string]NodeStep, error) {
	operation, target, from, err := currentOperationState(state)
	if err != nil {
		return nil, err
	}
	result := make(map[string]NodeStep)
	if operation.Phase == OperationFailed {
		return result, nil
	}
	pendingServers := sortedSteps(operation, target, NodeActionInstall, layout.RoleServer, StepPending, StepFailed)
	runningServers := sortedSteps(operation, target, NodeActionInstall, layout.RoleServer, StepRunning)
	if len(runningServers) > 0 {
		id := runningServers[0]
		result[id] = operation.NodeSteps[id]
		return result, nil
	}
	if len(pendingServers) > 0 {
		id := pendingServers[0]
		result[id] = operation.NodeSteps[id]
		return result, nil
	}

	runningAgents := sortedSteps(operation, target, NodeActionInstall, layout.RoleAgent,
		StepRunning)
	for _, id := range runningAgents {
		result[id] = operation.NodeSteps[id]
	}
	slots := max(0, maxParallelAgents-len(runningAgents))
	for _, id := range sortedSteps(operation, target, NodeActionInstall, layout.RoleAgent,
		StepPending, StepFailed) {
		if slots == 0 {
			break
		}
		result[id] = operation.NodeSteps[id]
		slots--
	}
	if len(result) > 0 {
		return result, nil
	}
	if !allActionComplete(operation, NodeActionInstall) {
		return result, nil
	}

	for _, id := range sortedSteps(operation, target, NodeActionCapabilities, "",
		StepPending, StepRunning, StepFailed) {
		if len(result) >= maxParallelAgents {
			break
		}
		result[id] = operation.NodeSteps[id]
	}
	if len(result) > 0 || !allActionComplete(operation, NodeActionCapabilities) {
		return result, nil
	}

	// Upgrades run strictly one node at a time, servers first: each moves
	// the host's k3s and restarts its own hostd, so two at once would take
	// two nodes out of service together.
	if running := sortedSteps(operation, target, NodeActionUpgrade, "", StepRunning); len(running) > 0 {
		result[running[0]] = operation.NodeSteps[running[0]]
		return result, nil
	}
	for _, role := range []string{layout.RoleServer, layout.RoleAgent} {
		if pending := sortedSteps(operation, target, NodeActionUpgrade, role,
			StepPending, StepFailed); len(pending) > 0 {
			result[pending[0]] = operation.NodeSteps[pending[0]]
			return result, nil
		}
	}
	if !allActionComplete(operation, NodeActionUpgrade) {
		return result, nil
	}

	// Platform convergence is leader-local and is advanced separately.
	if target.Platform.Enabled && operation.Phase != OperationRemoving &&
		operation.Phase != OperationRebalancing && operation.Phase != OperationVerifying &&
		operation.Phase != OperationComplete {
		return result, nil
	}

	for _, id := range sortedRemovalSteps(operation, from) {
		step := operation.NodeSteps[id]
		if step.Phase == StepPending || step.Phase == StepRunning || step.Phase == StepFailed {
			if from.Nodes[id].Role == layout.RoleServer &&
				operation.EtcdSnapshot != StepComplete {
				continue
			}
			result[id] = step
			// Server removals stay serial; agents may be returned together.
			if from.Nodes[id].Role == layout.RoleServer {
				break
			}
			if len(result) >= maxParallelAgents {
				break
			}
		}
	}
	return result, nil
}

func MarkEtcdSnapshot(state *State, succeeded bool, lastError string, now time.Time) error {
	operation, _, _, err := currentOperationState(state)
	if err != nil {
		return err
	}
	now = now.UTC().Truncate(time.Second)
	if succeeded {
		operation.EtcdSnapshot = StepComplete
		operation.LastError = ""
	} else {
		operation.EtcdSnapshot = StepFailed
		operation.LastError = lastError
		operation.Phase = OperationFailed
	}
	operation.UpdatedAt = now
	state.Operations[operation.ID] = operation
	return nil
}

func NeedsEtcdSnapshot(state *State) bool {
	operation, _, from, err := currentOperationState(state)
	if err != nil || operation.EtcdSnapshot == StepComplete {
		return false
	}
	for id, step := range operation.NodeSteps {
		if step.Action == NodeActionRemove && from.Nodes[id].Role == layout.RoleServer {
			return true
		}
	}
	return false
}

func MarkTopologyActivated(state *State, now time.Time) error {
	operation, _, _, err := currentOperationState(state)
	if err != nil {
		return err
	}
	operation.TopologyActivated = true
	operation.Phase = OperationActivating
	operation.UpdatedAt = now.UTC().Truncate(time.Second)
	state.Operations[operation.ID] = operation
	return nil
}

func MarkNodeRemovalPrepared(state *State, nodeID string, now time.Time) error {
	operation, ok := state.Operations[state.CurrentOperation]
	if !ok {
		return errors.New("no active cluster operation")
	}
	step, ok := operation.NodeSteps[nodeID]
	if !ok || step.Action != NodeActionRemove {
		return fmt.Errorf("node %s has no removal in the active operation", nodeID)
	}
	now = now.UTC().Truncate(time.Second)
	step.ClusterPrepared = true
	step.LastError = ""
	step.UpdatedAt = now
	operation.NodeSteps[nodeID] = step
	operation.Phase = OperationRemoving
	operation.UpdatedAt = now
	state.Operations[operation.ID] = operation

	node := state.Nodes[nodeID]
	node.Phase = NodePhaseAwaitingCleanup
	node.LastAction = NodeActionRemove
	node.LastError = ""
	node.UpdatedAt = now
	state.Nodes[nodeID] = node
	return nil
}

func StartNodeAction(state *State, nodeID, attemptID string, now time.Time) error {
	operation, ok := state.Operations[state.CurrentOperation]
	if !ok {
		return errors.New("no active cluster operation")
	}
	step, ok := operation.NodeSteps[nodeID]
	if !ok {
		return fmt.Errorf("node %s has no action in the active operation", nodeID)
	}
	if step.Phase == StepComplete {
		return nil
	}
	wasFailed := step.Phase == StepFailed
	step.Phase = StepRunning
	if step.AttemptID == "" || wasFailed {
		step.AttemptID = attemptID
	}
	step.LastError = ""
	step.UpdatedAt = now.UTC().Truncate(time.Second)
	operation.NodeSteps[nodeID] = step
	operation.UpdatedAt = step.UpdatedAt
	operation.Phase = phaseForAction(step.Action)
	state.Operations[operation.ID] = operation
	if step.Action == NodeActionRemove {
		node := state.Nodes[nodeID]
		node.Phase = NodePhaseDraining
		node.LastAction = step.Action
		node.LastError = ""
		node.UpdatedAt = step.UpdatedAt
		state.Nodes[nodeID] = node
	}
	return nil
}

func CompleteNodeAction(state *State, nodeID, attemptID string, succeeded bool,
	lastError string, now time.Time) error {
	operation, ok := state.Operations[state.CurrentOperation]
	if !ok {
		return errors.New("no active cluster operation")
	}
	step, ok := operation.NodeSteps[nodeID]
	if !ok {
		return fmt.Errorf("node %s has no action in the active operation", nodeID)
	}
	if step.AttemptID != "" && attemptID != "" && step.AttemptID != attemptID {
		return errors.New("agent reported a stale action attempt")
	}
	if succeeded {
		step.Phase = StepComplete
		step.LastError = ""
	} else {
		step.Phase = StepFailed
		step.LastError = lastError
		operation.LastError = lastError
		operation.Phase = OperationFailed
	}
	step.UpdatedAt = now.UTC().Truncate(time.Second)
	operation.NodeSteps[nodeID] = step
	operation.UpdatedAt = step.UpdatedAt
	state.Operations[operation.ID] = operation

	node := state.Nodes[nodeID]
	node.LastAction = step.Action
	node.LastError = step.LastError
	node.UpdatedAt = step.UpdatedAt
	if succeeded {
		switch step.Action {
		case NodeActionInstall, NodeActionCapabilities, NodeActionUpgrade:
			node.Phase = NodePhaseActive
		case NodeActionRemove:
			node.Phase = NodePhaseAwaitingCleanup
		}
	} else {
		node.Phase = NodePhaseFailed
	}
	state.Nodes[nodeID] = node
	return nil
}

func CompleteNodeCleanup(state *State, nodeID string, now time.Time) error {
	node, ok := state.Nodes[nodeID]
	if !ok {
		return fmt.Errorf("node %s is not enrolled", nodeID)
	}
	if node.Phase != NodePhaseUninstalling &&
		node.Phase != NodePhaseAwaitingCleanup &&
		node.Phase != NodePhaseRemoved {
		return fmt.Errorf("node %s is not awaiting local cleanup", node.Name)
	}
	node.Phase = NodePhaseRemoved
	node.LastAction = "cleanup"
	node.LastError = ""
	node.UpdatedAt = now.UTC().Truncate(time.Second)
	state.Nodes[nodeID] = node
	return nil
}

func AdvancePlatform(state *State, succeeded bool, lastError string, now time.Time) error {
	operation, target, _, err := currentOperationState(state)
	if err != nil {
		return err
	}
	if !allActionComplete(operation, NodeActionInstall) ||
		!allActionComplete(operation, NodeActionCapabilities) ||
		!allActionComplete(operation, NodeActionUpgrade) {
		return errors.New("platform cannot reconcile before additions, capability changes, and upgrades complete")
	}
	now = now.UTC().Truncate(time.Second)
	if !succeeded {
		operation.Phase = OperationFailed
		operation.LastError = lastError
	} else if hasIncompleteAction(operation, NodeActionRemove) {
		operation.Phase = OperationRemoving
		operation.LastError = ""
	} else if operation.RebalanceWorkloads {
		operation.Phase = OperationRebalancing
		operation.LastError = ""
	} else {
		operation.Phase = OperationVerifying
		operation.LastError = ""
	}
	operation.UpdatedAt = now
	state.Operations[operation.ID] = operation
	_ = target
	return nil
}

// AwaitPlatformInitialization records the intentional hand-off to
// `cluster init`. It is not a failure: the long-lived coordinator cannot
// collect the one-time administrator credentials, so it waits here until
// Init has published the first bundle record.
func AwaitPlatformInitialization(state *State, now time.Time) error {
	operation, target, _, err := currentOperationState(state)
	if err != nil {
		return err
	}
	if !target.Platform.Enabled {
		return errors.New("platform initialization was requested for a disabled target")
	}
	if !allActionComplete(operation, NodeActionInstall) ||
		!allActionComplete(operation, NodeActionCapabilities) ||
		!allActionComplete(operation, NodeActionUpgrade) {
		return errors.New("platform initialization cannot begin before topology actions complete")
	}
	now = now.UTC().Truncate(time.Second)
	operation.Phase = OperationInitializing
	operation.LastError = ""
	operation.UpdatedAt = now
	state.Operations[operation.ID] = operation
	return nil
}

// MarkUpgradePreflight records the leader's one-time release check for the
// active operation.
func MarkUpgradePreflight(state *State, now time.Time) error {
	operation, _, _, err := currentOperationState(state)
	if err != nil {
		return err
	}
	operation.UpgradePreflight = true
	operation.UpdatedAt = now.UTC().Truncate(time.Second)
	state.Operations[operation.ID] = operation
	return nil
}

// HeartbeatWindow is how recently every node must have polled before a
// version change is accepted: an upgrade waits on each host in turn, so a
// silent one would stall it at the first step.
const HeartbeatWindow = 2 * time.Minute

// RequestPlatformVersion stages and freezes a platform version change in
// one step: the candidate becomes the converged topology with the new
// version, and the resulting operation upgrades every node and reconverges
// the bundle. It is the only mutation the product daemon performs on the
// cluster state, so every refusal a careful operator would make is made
// here: no concurrent operation, an initialized platform, a newer tagged
// release, no staged topology edits that would ride along unreviewed, and
// every node active and recently heard from.
// Updatable reports why a version change cannot start right now, or nil:
// the console shows the reason and disables the button before anyone asks.
func Updatable(state *State, now time.Time) error {
	if state.CurrentOperation != "" {
		return ErrOperationActive
	}
	converged, err := state.Converged()
	if err != nil {
		return err
	}
	if !converged.Platform.Enabled {
		return errors.New("the platform is not initialized; run skali cluster init first")
	}
	if state.CandidateRevision != state.ConvergedRevision {
		return errors.New("topology changes are staged but not applied; apply or discard them before updating")
	}
	for _, node := range SortedRevisionNodes(converged.Nodes) {
		enrolled, ok := state.Nodes[node.ID]
		if !ok || enrolled.Phase != NodePhaseActive {
			return fmt.Errorf("node %s is not active", node.Name)
		}
		if enrolled.LastSeen.IsZero() || now.Sub(enrolled.LastSeen) > HeartbeatWindow {
			return fmt.Errorf("node %s has not reported to the coordinator recently", node.Name)
		}
	}
	return nil
}

func RequestPlatformVersion(state *State, target string, now time.Time) (Plan, Operation, error) {
	if !version.IsRelease(target) {
		return Plan{}, Operation{}, fmt.Errorf("version %q is not a tagged release", target)
	}
	if err := Updatable(state, now); err != nil {
		return Plan{}, Operation{}, err
	}
	converged, err := state.Converged()
	if err != nil {
		return Plan{}, Operation{}, err
	}
	if converged.Platform.Version != "" && !version.Older(converged.Platform.Version, target) {
		return Plan{}, Operation{}, fmt.Errorf("the platform already runs %s; %s is not newer",
			converged.Platform.Version, target)
	}
	if _, err := state.EditCandidate(now, func(_ map[string]RevisionNode, platform *PlatformState) error {
		platform.Version = target
		return nil
	}); err != nil {
		return Plan{}, Operation{}, err
	}
	return FreezeCandidate(state, false, now)
}

func FailOperation(state *State, lastError string, now time.Time) error {
	if state.CurrentOperation == "" {
		return nil
	}
	operation, ok := state.Operations[state.CurrentOperation]
	if !ok {
		return errors.New("active operation is missing")
	}
	if operation.Phase == OperationComplete {
		return nil
	}
	operation.Phase = OperationFailed
	operation.LastError = lastError
	operation.UpdatedAt = now.UTC().Truncate(time.Second)
	state.Operations[operation.ID] = operation
	return nil
}

func CompleteOperation(state *State, now time.Time) error {
	operation, target, _, err := currentOperationState(state)
	if err != nil {
		return err
	}
	for _, step := range operation.NodeSteps {
		if step.Phase != StepComplete {
			return errors.New("cluster operation still has incomplete node actions")
		}
	}
	now = now.UTC().Truncate(time.Second)
	operation.Phase = OperationComplete
	operation.LastError = ""
	operation.UpdatedAt = now
	operation.CompletedAt = now
	state.Operations[operation.ID] = operation
	state.ConvergedRevision = target.ID
	state.Platform = target.Platform
	state.TargetRevision = ""
	state.CurrentOperation = ""
	if state.CandidateRevision == target.ID {
		state.CandidateRevision = target.ID
	}
	state.UpdatedAt = now
	return nil
}

func currentOperationState(state *State) (Operation, Revision, Revision, error) {
	if state.CurrentOperation == "" {
		return Operation{}, Revision{}, Revision{}, errors.New("cluster has no active operation")
	}
	operation, ok := state.Operations[state.CurrentOperation]
	if !ok {
		return Operation{}, Revision{}, Revision{}, errors.New("active operation is missing")
	}
	target, err := state.revision(operation.TargetRevision, "operation target")
	if err != nil {
		return Operation{}, Revision{}, Revision{}, err
	}
	from, err := state.revision(operation.FromRevision, "operation source")
	return operation, target, from, err
}

func sortedSteps(operation Operation, revision Revision, action, role string, phases ...string) []string {
	var ids []string
	for id, step := range operation.NodeSteps {
		node, exists := revision.Nodes[id]
		if !exists || step.Action != action || (role != "" && node.Role != role) ||
			!slices.Contains(phases, step.Phase) {
			continue
		}
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b string) int {
		if revision.Nodes[a].Name < revision.Nodes[b].Name {
			return -1
		}
		if revision.Nodes[a].Name > revision.Nodes[b].Name {
			return 1
		}
		return 0
	})
	return ids
}

func sortedRemovalSteps(operation Operation, from Revision) []string {
	var agents, servers []string
	for id, step := range operation.NodeSteps {
		if step.Action != NodeActionRemove {
			continue
		}
		if from.Nodes[id].Role == layout.RoleServer {
			servers = append(servers, id)
		} else {
			agents = append(agents, id)
		}
	}
	sortByName := func(ids []string) {
		slices.SortFunc(ids, func(a, b string) int {
			if from.Nodes[a].Name < from.Nodes[b].Name {
				return -1
			}
			return 1
		})
	}
	sortByName(agents)
	sortByName(servers)
	return append(agents, servers...)
}

func allActionComplete(operation Operation, action string) bool {
	for _, step := range operation.NodeSteps {
		if step.Action == action && step.Phase != StepComplete {
			return false
		}
	}
	return true
}

func hasIncompleteAction(operation Operation, action string) bool {
	for _, step := range operation.NodeSteps {
		if step.Action == action && step.Phase != StepComplete {
			return true
		}
	}
	return false
}

func phaseForAction(action string) string {
	switch action {
	case NodeActionInstall:
		return OperationAdding
	case NodeActionCapabilities:
		return OperationActivating
	case NodeActionUpgrade:
		return OperationUpgrading
	case NodeActionRemove:
		return OperationRemoving
	default:
		return OperationPending
	}
}
