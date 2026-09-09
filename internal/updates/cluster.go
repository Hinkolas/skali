package updates

import (
	"context"
	"errors"
	"sort"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"

	"github.com/Hinkolas/skali/internal/clusterstate"
)

// ErrNotManaged means there is no reconciled cluster state to drive: the
// local dev platform, a legacy installation, or an API-only daemon. Versions
// can still be scanned and shown; the move must run through the CLI.
var ErrNotManaged = errors.New("this installation is not managed by a cluster coordinator")

// Cluster is the bridge to the installer-owned cluster state. It reads the
// coordination document skalid already has RBAC for and writes exactly one
// thing into it: a requested platform version.
type Cluster struct {
	Client kubernetes.Interface
	now    func() time.Time
}

// NodeState is one enrolled node as the coordinator sees it.
type NodeState struct {
	ID                  string    `json:"id"`
	Name                string    `json:"name"`
	Role                string    `json:"role"`
	K3sVersion          string    `json:"k3s_version,omitempty"`
	AgentVersion        string    `json:"agent_version,omitempty"`
	Phase               string    `json:"phase"`
	LastSeen            time.Time `json:"last_seen,omitzero"`
	CoordinatorVersion  string    `json:"coordinator_version,omitempty"`
	CoordinatorLastSeen time.Time `json:"coordinator_last_seen,omitzero"`
}

// StepState is one node's part of an operation.
type StepState struct {
	NodeID string `json:"node_id"`
	Node   string `json:"node"`
	Action string `json:"action"`
	Phase  string `json:"phase"`
	Error  string `json:"error,omitempty"`
}

// OperationState projects a cluster operation for the console: the phases
// and per-node steps the coordinator journals, plus the version it moves to.
type OperationState struct {
	ID            string      `json:"id"`
	Phase         string      `json:"phase"`
	TargetVersion string      `json:"target_version,omitempty"`
	FromVersion   string      `json:"from_version,omitempty"`
	StartedAt     time.Time   `json:"started_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
	CompletedAt   *time.Time  `json:"completed_at,omitempty"`
	Error         string      `json:"error,omitempty"`
	Steps         []StepState `json:"steps"`
}

// Settled reports whether the operation finished, either way.
func (o *OperationState) Settled() bool {
	return o == nil || o.Phase == clusterstate.OperationComplete || o.Phase == clusterstate.OperationFailed
}

// Snapshot is what the console needs from the cluster state.
type Snapshot struct {
	ExpectedK3s string
	// PlatformVersion is the release the converged revision names; empty
	// on clusters initialized by a dev build.
	PlatformVersion string
	Nodes           []NodeState
	// Operation is the current operation, or the most recent update when
	// none is running, so a finished or failed update stays visible.
	Operation      *OperationState
	LastSuccessful *OperationState
	// Manageable is whether a version change could start now; Reason says
	// why not.
	Manageable bool
	Reason     string
}

func (c *Cluster) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *Cluster) store() (*clusterstate.Store, error) {
	if c == nil || c.Client == nil {
		return nil, ErrNotManaged
	}
	return &clusterstate.Store{Client: c.Client}, nil
}

// Snapshot reads the cluster state; ErrNotManaged when there is none.
func (c *Cluster) Snapshot(ctx context.Context) (*Snapshot, error) {
	store, err := c.store()
	if err != nil {
		return nil, err
	}
	state, err := store.Load(ctx)
	if err != nil {
		if apierrors.IsNotFound(errors.Unwrap(err)) || apierrors.IsNotFound(err) ||
			apierrors.IsForbidden(errors.Unwrap(err)) {
			return nil, ErrNotManaged
		}
		return nil, err
	}
	return project(state, c.clock()), nil
}

// Apply requests the platform version; the coordinator takes it from there.
func (c *Cluster) Apply(ctx context.Context, target string) (*Snapshot, error) {
	store, err := c.store()
	if err != nil {
		return nil, err
	}
	state, err := store.Update(ctx, func(state *clusterstate.State) error {
		_, err := clusterstate.RequestRelease(state, target, c.clock())
		return err
	})
	if err != nil {
		return nil, err
	}
	return project(state, c.clock()), nil
}

func project(state *clusterstate.State, now time.Time) *Snapshot {
	snapshot := &Snapshot{Manageable: true}
	if converged, err := state.Converged(); err == nil {
		snapshot.PlatformVersion = converged.Platform.Version
		for _, release := range state.Updates.Operations {
			if release.Version == converged.Platform.Version && release.K3s != "" {
				snapshot.ExpectedK3s = release.K3s
			}
		}
	}
	for _, node := range clusterstate.SortedNodes(state.Nodes) {
		// Retired identities remain in the journal, but their hostnames may
		// already belong to a new enrollment.
		if node.Phase == clusterstate.NodePhaseRemoved || node.Phase == clusterstate.NodePhaseCancelled {
			continue
		}
		coordinator := clusterstate.CoordinatorReport{}
		if node.Role == "server" {
			coordinator = state.Updates.Coordinators[node.ID]
		}
		snapshot.Nodes = append(snapshot.Nodes, NodeState{
			ID: node.ID, Name: node.Name, Role: node.Role, K3sVersion: node.K3sVersion,
			AgentVersion: node.AgentVersion, Phase: node.Phase, LastSeen: node.LastSeen,
			CoordinatorVersion:  coordinator.Version,
			CoordinatorLastSeen: coordinator.LastSeen,
		})
	}
	if err := clusterstate.Updatable(state, now); err != nil {
		snapshot.Manageable = false
		snapshot.Reason = err.Error()
	}
	snapshot.Operation = relevantOperation(state)
	for _, op := range state.Operations {
		if op.Phase == clusterstate.OperationComplete && clusterstate.IsReleaseOperation(state, op) &&
			(snapshot.LastSuccessful == nil || snapshot.LastSuccessful.UpdatedAt.Before(op.UpdatedAt)) {
			snapshot.LastSuccessful = projectOperation(state, op)
		}
	}
	return snapshot
}

// relevantOperation is the running operation, else the newest one that
// changed the platform version.
func relevantOperation(state *clusterstate.State) *OperationState {
	if state.CurrentOperation != "" {
		if operation, ok := state.Operations[state.CurrentOperation]; ok {
			if clusterstate.IsReleaseOperation(state, operation) {
				return projectOperation(state, operation)
			}
		}
	}
	var newest *clusterstate.Operation
	for id := range state.Operations {
		operation := state.Operations[id]
		if !clusterstate.IsReleaseOperation(state, operation) {
			continue
		}
		if newest == nil || newest.StartedAt.Before(operation.StartedAt) {
			newest = &operation
		}
	}
	if newest == nil {
		return nil
	}
	return projectOperation(state, *newest)
}

func projectOperation(state *clusterstate.State, operation clusterstate.Operation) *OperationState {
	from, target := state.Revisions[operation.FromRevision], state.Revisions[operation.TargetRevision]
	projected := &OperationState{
		ID: operation.ID, Phase: operation.Phase,
		TargetVersion: target.Platform.Version, FromVersion: from.Platform.Version,
		StartedAt: operation.StartedAt, UpdatedAt: operation.UpdatedAt,
		Error: operation.LastError, Steps: []StepState{},
	}
	if !operation.CompletedAt.IsZero() {
		completed := operation.CompletedAt
		projected.CompletedAt = &completed
	}
	for id, step := range operation.NodeSteps {
		name := id
		if node, ok := target.Nodes[id]; ok {
			name = node.Name
		} else if node, ok := from.Nodes[id]; ok {
			name = node.Name
		}
		projected.Steps = append(projected.Steps, StepState{
			NodeID: id, Node: name, Action: step.Action, Phase: step.Phase, Error: step.LastError,
		})
	}
	// Servers first, then by name: the order the coordinator runs them.
	roleOf := func(id string) string {
		if node, ok := target.Nodes[id]; ok {
			return node.Role
		}
		if node, ok := from.Nodes[id]; ok {
			return node.Role
		}
		return ""
	}
	sort.Slice(projected.Steps, func(i, j int) bool {
		a, b := projected.Steps[i], projected.Steps[j]
		if roleOf(a.NodeID) != roleOf(b.NodeID) {
			return roleOf(a.NodeID) == "server"
		}
		if a.Node == b.Node {
			return a.NodeID < b.NodeID
		}
		return a.Node < b.Node
	})
	return projected
}
