// Package clusterstate defines the installer-owned desired state for a
// reconciled Skali cluster. It deliberately has no dependency on skalid or
// its product database: the state exists as soon as the seed k3s server is
// ready and remains available while the product bundle is absent or down.
package clusterstate

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/layout"
)

const (
	Namespace       = "skali-cluster-system"
	StateName       = "skali-cluster-state"
	StateKey        = "state.json"
	TrustSecretName = "skali-coordinator-trust"
	InvitationLabel = "skali.dev/invitation"
	RevisionLabel   = "skali.dev/cluster-revision"
	NodeLabel       = "skali.dev/enrolled-node"
	OperationLabel  = "skali.dev/reconciliation-operation"

	CurrentVersion         = 1
	DefaultCoordinatorPort = "6444"

	NodeDesiredPresent = "present"
	NodeDesiredAbsent  = "absent"

	NodePhaseEnrolled        = "enrolled"
	NodePhaseAwaitingApply   = "awaiting-apply"
	NodePhaseInstalling      = "installing"
	NodePhaseStarting        = "starting"
	NodePhaseJoined          = "joined"
	NodePhaseActive          = "active"
	NodePhaseDraining        = "draining"
	NodePhaseUninstalling    = "uninstalling"
	NodePhaseAwaitingCleanup = "awaiting-node-cleanup"
	NodePhaseRemoved         = "removed"
	NodePhaseFailed          = "failed"

	OperationPending      = "pending"
	OperationAdding       = "adding-nodes"
	OperationActivating   = "activating-topology"
	OperationInitializing = "awaiting-platform-initialization"
	OperationPlatform     = "reconciling-platform"
	OperationRemoving     = "removing-nodes"
	OperationRebalancing  = "rebalancing-workloads"
	OperationVerifying    = "verifying"
	OperationComplete     = "complete"
	OperationFailed       = "failed"
)

// State is the small, cluster-wide coordination document. Revisions remain
// immutable once created; the three revision references are advanced with a
// Kubernetes resource-version compare-and-swap by Store.Update.
type State struct {
	Version  int    `json:"version"`
	Cluster  string `json:"cluster"`
	Sequence int64  `json:"sequence"`

	ConvergedRevision    string `json:"convergedRevision"`
	TargetRevision       string `json:"targetRevision,omitempty"`
	CandidateRevision    string `json:"candidateRevision"`
	CurrentOperation     string `json:"currentOperation,omitempty"`
	Decommissioning      bool   `json:"decommissioning,omitempty"`
	ReconciliationPaused bool   `json:"reconciliationPaused,omitempty"`

	Platform PlatformState `json:"platform"`

	Revisions  map[string]Revision  `json:"revisions"`
	Nodes      map[string]Node      `json:"nodes"`
	Operations map[string]Operation `json:"operations,omitempty"`
	CreatedAt  time.Time            `json:"createdAt"`
	UpdatedAt  time.Time            `json:"updatedAt"`
}

type PlatformState struct {
	Enabled      bool   `json:"enabled"`
	RegistryNode string `json:"registryNode,omitempty"`
}

// Revision is an immutable desired topology snapshot. Nodes absent from the
// map are desired removed; Node.Desired is retained for readable transition
// snapshots and future wire compatibility.
type Revision struct {
	ID        string                  `json:"id"`
	Parent    string                  `json:"parent,omitempty"`
	Sequence  int64                   `json:"sequence"`
	Nodes     map[string]RevisionNode `json:"nodes"`
	Platform  PlatformState           `json:"platform"`
	CreatedAt time.Time               `json:"createdAt"`
}

type RevisionNode struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Role         string   `json:"role"`
	Capabilities []string `json:"capabilities"`
	Desired      string   `json:"desired,omitempty"`
}

// Node combines immutable enrollment identity with observed host state.
// Credentials are never represented here.
type Node struct {
	ID             string    `json:"id"`
	InstallationID string    `json:"installationId"`
	Name           string    `json:"name"`
	Role           string    `json:"role"`
	Capabilities   []string  `json:"capabilities"`
	NodeIP         string    `json:"nodeIP,omitempty"`
	Coordinator    string    `json:"coordinator,omitempty"`
	AgentVersion   string    `json:"agentVersion,omitempty"`
	K3sVersion     string    `json:"k3sVersion,omitempty"`
	Phase          string    `json:"phase"`
	LastAction     string    `json:"lastAction,omitempty"`
	LastError      string    `json:"lastError,omitempty"`
	LastSeen       time.Time `json:"lastSeen,omitempty"`
	EnrolledAt     time.Time `json:"enrolledAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type Operation struct {
	ID                 string              `json:"id"`
	FromRevision       string              `json:"fromRevision"`
	TargetRevision     string              `json:"targetRevision"`
	Phase              string              `json:"phase"`
	RebalanceWorkloads bool                `json:"rebalanceWorkloads,omitempty"`
	EtcdSnapshot       string              `json:"etcdSnapshot,omitempty"`
	TopologyActivated  bool                `json:"topologyActivated,omitempty"`
	NodeSteps          map[string]NodeStep `json:"nodeSteps,omitempty"`
	LastError          string              `json:"lastError,omitempty"`
	StartedAt          time.Time           `json:"startedAt"`
	UpdatedAt          time.Time           `json:"updatedAt"`
	CompletedAt        time.Time           `json:"completedAt,omitempty"`
}

type NodeStep struct {
	Action    string `json:"action"`
	Phase     string `json:"phase"`
	AttemptID string `json:"attemptId,omitempty"`
	// ClusterPrepared records the leader-side drain and Node/member
	// deletion separately from the remote host cleanup. This lets an
	// offline removed host stop blocking cluster convergence while still
	// requiring an explicit forced forget to waive local cleanup.
	ClusterPrepared bool      `json:"clusterPrepared,omitempty"`
	LastError       string    `json:"lastError,omitempty"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// NewSeedState creates revision one with the already-running seed server and
// the platform disabled.
func NewSeedState(cluster string, seed Node, now time.Time) (*State, error) {
	if cluster == "" || seed.ID == "" || seed.Name == "" || seed.InstallationID == "" {
		return nil, errors.New("cluster state seed is missing identity")
	}
	if seed.Role != layout.RoleServer {
		return nil, errors.New("cluster state seed must be a server")
	}
	now = now.UTC().Truncate(time.Second)
	seed.Phase = NodePhaseActive
	seed.EnrolledAt = now
	seed.UpdatedAt = now
	revisionID := uuid.NewString()
	revisionNode := revisionNode(seed)
	revision := Revision{
		ID: revisionID, Sequence: 1, CreatedAt: now,
		Platform: PlatformState{},
		Nodes:    map[string]RevisionNode{seed.ID: revisionNode},
	}
	return &State{
		Version: CurrentVersion, Cluster: cluster, Sequence: 1,
		ConvergedRevision: revisionID, CandidateRevision: revisionID,
		Platform:   PlatformState{},
		Revisions:  map[string]Revision{revisionID: revision},
		Nodes:      map[string]Node{seed.ID: seed},
		Operations: make(map[string]Operation),
		CreatedAt:  now, UpdatedAt: now,
	}, nil
}

func revisionNode(node Node) RevisionNode {
	capabilities := append([]string(nil), node.Capabilities...)
	sort.Strings(capabilities)
	return RevisionNode{
		ID: node.ID, Name: node.Name, Role: node.Role,
		Capabilities: capabilities, Desired: NodeDesiredPresent,
	}
}

func (s *State) Candidate() (Revision, error) {
	return s.revision(s.CandidateRevision, "candidate")
}

func (s *State) Converged() (Revision, error) {
	return s.revision(s.ConvergedRevision, "converged")
}

func (s *State) Target() (Revision, error) {
	if s.TargetRevision == "" {
		return Revision{}, errors.New("cluster has no target revision")
	}
	return s.revision(s.TargetRevision, "target")
}

func (s *State) revision(id, kind string) (Revision, error) {
	revision, ok := s.Revisions[id]
	if !ok {
		return Revision{}, fmt.Errorf("%s revision %q is missing", kind, id)
	}
	return revision, nil
}

// EditCandidate snapshots the current candidate after applying edit. A
// candidate created during an in-flight apply is based on the target, so new
// changes never discard work already authorized by the operator.
func (s *State) EditCandidate(now time.Time, edit func(nodes map[string]RevisionNode, platform *PlatformState) error) (Revision, error) {
	baseID := s.CandidateRevision
	if s.TargetRevision != "" && baseID == s.ConvergedRevision {
		baseID = s.TargetRevision
	}
	base, ok := s.Revisions[baseID]
	if !ok {
		return Revision{}, fmt.Errorf("candidate base revision %q is missing", baseID)
	}
	nodes := make(map[string]RevisionNode, len(base.Nodes))
	for id, node := range base.Nodes {
		node.Capabilities = append([]string(nil), node.Capabilities...)
		nodes[id] = node
	}
	platform := base.Platform
	if err := edit(nodes, &platform); err != nil {
		return Revision{}, err
	}
	if err := validateRevision(nodes, platform); err != nil {
		return Revision{}, err
	}
	now = now.UTC().Truncate(time.Second)
	s.Sequence++
	revision := Revision{
		ID: uuid.NewString(), Parent: base.ID, Sequence: s.Sequence,
		Nodes: nodes, Platform: platform, CreatedAt: now,
	}
	if s.Revisions == nil {
		s.Revisions = make(map[string]Revision)
	}
	s.Revisions[revision.ID] = revision
	s.CandidateRevision = revision.ID
	s.UpdatedAt = now
	return revision, nil
}

// ReplaceCandidateLayout applies the optional automation document to
// already-enrolled identities. Unknown hosts and role changes are rejected;
// credentials and enrollment remain an explicit per-host step.
func (s *State) ReplaceCandidateLayout(desired layout.Layout,
	now time.Time) (Revision, error) {
	if desired.Name != s.Cluster {
		return Revision{}, fmt.Errorf("layout belongs to cluster %q, not %q",
			desired.Name, s.Cluster)
	}
	known := make(map[string]Node, len(s.Nodes))
	for _, node := range s.Nodes {
		if node.Phase != NodePhaseRemoved && node.Phase != NodePhaseUninstalling {
			known[node.Name] = node
		}
	}
	return s.EditCandidate(now, func(nodes map[string]RevisionNode,
		_ *PlatformState) error {
		replacement := make(map[string]RevisionNode, len(desired.Nodes))
		for name, wanted := range desired.Nodes {
			enrolled, ok := known[name]
			if !ok {
				return fmt.Errorf("layout node %s has not enrolled; enroll unknown hosts before importing a layout",
					name)
			}
			if enrolled.Role != wanted.Role {
				return fmt.Errorf("layout changes immutable role of %s from %s to %s; remove and re-enroll it",
					name, enrolled.Role, wanted.Role)
			}
			replacement[enrolled.ID] = RevisionNode{
				ID: enrolled.ID, Name: name, Role: wanted.Role,
				Capabilities: append([]string(nil), wanted.Capabilities...),
				Desired:      NodeDesiredPresent,
			}
		}
		clear(nodes)
		for id, node := range replacement {
			nodes[id] = node
		}
		return nil
	})
}

func validateRevision(nodes map[string]RevisionNode, platform PlatformState) error {
	if len(nodes) == 0 {
		return errors.New("a cluster revision must contain at least one node")
	}
	names := make(map[string]string, len(nodes))
	servers := 0
	capable := make(map[string]bool)
	for id, node := range nodes {
		if id == "" || node.ID != id || node.Name == "" {
			return errors.New("cluster revision contains a node with incomplete identity")
		}
		if other := names[node.Name]; other != "" && other != id {
			return fmt.Errorf("node name %q is already used", node.Name)
		}
		names[node.Name] = id
		if node.Role == layout.RoleServer {
			servers++
		} else if node.Role != layout.RoleAgent {
			return fmt.Errorf("node %s has invalid role %q", node.Name, node.Role)
		}
		seen := make(map[string]bool)
		for _, capability := range node.Capabilities {
			if !slices.Contains(layout.Capabilities, capability) {
				return fmt.Errorf("node %s has unknown capability %q", node.Name, capability)
			}
			if seen[capability] {
				return fmt.Errorf("node %s repeats capability %q", node.Name, capability)
			}
			seen[capability] = true
			capable[capability] = true
		}
	}
	if servers == 0 {
		return errors.New("a cluster revision must retain at least one server")
	}
	if platform.Enabled {
		var missing []string
		for _, capability := range layout.RequiredCapabilities {
			if !capable[capability] {
				missing = append(missing, capability)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("platform-enabled revision is missing required capabilities: %s",
				strings.Join(missing, ", "))
		}
	}
	if platform.RegistryNode != "" {
		node, ok := nodes[platform.RegistryNode]
		if !ok || !slices.Contains(node.Capabilities, layout.CapabilityRegistry) {
			return errors.New("the registry data node must remain present and registry-capable")
		}
	}
	return nil
}

func FindNodeByName(nodes map[string]RevisionNode, name string) (RevisionNode, bool) {
	for _, node := range nodes {
		if node.Name == name {
			return node, true
		}
	}
	return RevisionNode{}, false
}

func SortedRevisionNodes(nodes map[string]RevisionNode) []RevisionNode {
	result := make([]RevisionNode, 0, len(nodes))
	for _, node := range nodes {
		result = append(result, node)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Role != result[j].Role {
			return result[i].Role == layout.RoleServer
		}
		return result[i].Name < result[j].Name
	})
	return result
}

func SortedNodes(nodes map[string]Node) []Node {
	result := make([]Node, 0, len(nodes))
	for _, node := range nodes {
		result = append(result, node)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Role != result[j].Role {
			return result[i].Role == layout.RoleServer
		}
		return result[i].Name < result[j].Name
	})
	return result
}
