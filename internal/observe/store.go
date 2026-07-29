// Package observe maintains the in-memory observed-state store: the freshest
// known projection of the cluster, fed by LIST/WATCH sources, rebuilt on
// every startup, and never persisted. Consumers read typed projections with
// explicit source freshness; nothing here is authoritative intent
// (REWORK_V2 sections 5.1, 5.3, 7).
package observe

import (
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/module"
)

// Object is one observed cluster object in projected form. Kind uses the
// module vocabulary plus the store-internal kinds "namespace" and "node"
// that never reach module evaluation.
type Object struct {
	Ref      kube.ObjectRef
	Kind     string
	Name     string // module-facing name: service key for workloads, object name otherwise
	Labels   map[string]string
	Environment uuid.UUID // uuid.Nil when the object is not environment-owned
	Service  string
	Revision string
	Node     string // pods only
	Generation int64

	// ManagedFields are retained for workload objects so the scale planner
	// can decide ownership transitions without a request-time read.
	ManagedFields []metav1.ManagedFieldsEntry

	Workload   *module.WorkloadStatus
	Pod        *module.PodStatus
	Autoscaler *module.AutoscalerStatus
	Claim      *module.ClaimStatus

	// SharedKey links platform-scoped objects (Environment == uuid.Nil,
	// e.g. a database pool) to the environment-owned objects that reference
	// them. Snapshots include the shared objects their environment
	// references, and shared-object changes fan out to every referencing
	// environment.
	SharedKey       string
	DatabaseCluster *module.DatabaseClusterStatus
	DatabaseTenant  *module.DatabaseTenantStatus
}

type objectKey struct {
	group     string
	kind      string
	namespace string
	name      string
}

func keyOf(ref kube.ObjectRef) objectKey {
	return objectKey{group: ref.GVK.Group, kind: ref.GVK.Kind, namespace: ref.Namespace, name: ref.Name}
}

// Store is the ObservedStore: an RWMutex-guarded set of typed indexes over
// the current cluster view plus explicit source freshness.
type Store struct {
	mu    sync.RWMutex
	clock func() time.Time

	objects          map[objectKey]*Object
	byEnvironment    map[uuid.UUID]map[objectKey]struct{}
	podsByNode       map[string]map[uuid.UUID]int
	nodeArch         map[string]string
	nodeCapabilities map[string]map[string]bool
	events           map[objectKey][]EventRecord
	sharedObjects    map[string]objectKey            // shared key -> platform object
	sharedRefs       map[string]map[uuid.UUID]int    // shared key -> referencing environments

	state      string // module.SourceUnknown | SourceFresh | SourceStale
	ready      bool
	lastSync   time.Time
	failedAt   time.Time
	staleSince time.Time

	broadcast *broadcaster
}

func NewStore(clock func() time.Time) *Store {
	if clock == nil {
		clock = time.Now
	}
	return &Store{
		clock:            clock,
		objects:          make(map[objectKey]*Object),
		byEnvironment:    make(map[uuid.UUID]map[objectKey]struct{}),
		podsByNode:       make(map[string]map[uuid.UUID]int),
		nodeArch:         make(map[string]string),
		nodeCapabilities: make(map[string]map[string]bool),
		events:           make(map[objectKey][]EventRecord),
		sharedObjects:    make(map[string]objectKey),
		sharedRefs:       make(map[string]map[uuid.UUID]int),
		state:            module.SourceUnknown,
		broadcast:        newBroadcaster(),
	}
}

// Upsert records one observed object and publishes an invalidation for its
// environment; a platform-scoped shared object fans out to every
// environment referencing it. The write side is the watch source or a test
// fake.
func (s *Store) Upsert(obj Object) {
	key := keyOf(obj.Ref)
	s.mu.Lock()
	previous := s.objects[key]
	if previous != nil {
		s.unindexLocked(key, previous)
	}
	stored := obj
	s.objects[key] = &stored
	s.indexLocked(key, &stored)
	var fanOut []uuid.UUID
	if obj.Environment == uuid.Nil && obj.SharedKey != "" {
		fanOut = s.environmentsForSharedKeyLocked(obj.SharedKey)
	}
	s.mu.Unlock()

	s.invalidate(obj.Environment)
	if previous != nil && previous.Environment != obj.Environment {
		s.invalidate(previous.Environment)
	}
	for _, environment := range fanOut {
		s.invalidate(environment)
	}
}

// Remove drops one observed object, if present.
func (s *Store) Remove(ref kube.ObjectRef) {
	key := keyOf(ref)
	s.mu.Lock()
	previous := s.objects[key]
	var fanOut []uuid.UUID
	if previous != nil {
		s.unindexLocked(key, previous)
		delete(s.objects, key)
		if previous.Environment == uuid.Nil && previous.SharedKey != "" {
			fanOut = s.environmentsForSharedKeyLocked(previous.SharedKey)
		}
	}
	s.mu.Unlock()
	if previous != nil {
		s.invalidate(previous.Environment)
	}
	for _, environment := range fanOut {
		s.invalidate(environment)
	}
}

func (s *Store) indexLocked(key objectKey, obj *Object) {
	if obj.Environment != uuid.Nil {
		if s.byEnvironment[obj.Environment] == nil {
			s.byEnvironment[obj.Environment] = make(map[objectKey]struct{})
		}
		s.byEnvironment[obj.Environment][key] = struct{}{}
		if obj.Kind == module.KindPod && obj.Node != "" {
			if s.podsByNode[obj.Node] == nil {
				s.podsByNode[obj.Node] = make(map[uuid.UUID]int)
			}
			s.podsByNode[obj.Node][obj.Environment]++
		}
		if obj.SharedKey != "" {
			if s.sharedRefs[obj.SharedKey] == nil {
				s.sharedRefs[obj.SharedKey] = make(map[uuid.UUID]int)
			}
			s.sharedRefs[obj.SharedKey][obj.Environment]++
		}
		return
	}
	if obj.SharedKey != "" {
		s.sharedObjects[obj.SharedKey] = key
	}
}

func (s *Store) unindexLocked(key objectKey, obj *Object) {
	if obj.Environment != uuid.Nil {
		if set := s.byEnvironment[obj.Environment]; set != nil {
			delete(set, key)
			if len(set) == 0 {
				delete(s.byEnvironment, obj.Environment)
			}
		}
		if obj.Kind == module.KindPod && obj.Node != "" {
			if counts := s.podsByNode[obj.Node]; counts != nil {
				counts[obj.Environment]--
				if counts[obj.Environment] <= 0 {
					delete(counts, obj.Environment)
				}
				if len(counts) == 0 {
					delete(s.podsByNode, obj.Node)
				}
			}
		}
		if obj.SharedKey != "" {
			if refs := s.sharedRefs[obj.SharedKey]; refs != nil {
				refs[obj.Environment]--
				if refs[obj.Environment] <= 0 {
					delete(refs, obj.Environment)
				}
				if len(refs) == 0 {
					delete(s.sharedRefs, obj.SharedKey)
				}
			}
		}
		return
	}
	if obj.SharedKey != "" && s.sharedObjects[obj.SharedKey] == key {
		delete(s.sharedObjects, obj.SharedKey)
	}
}

// environmentsForSharedKeyLocked lists the environments referencing one
// shared object, for fan-out invalidation.
func (s *Store) environmentsForSharedKeyLocked(sharedKey string) []uuid.UUID {
	refs := s.sharedRefs[sharedKey]
	environments := make([]uuid.UUID, 0, len(refs))
	for environment := range refs {
		environments = append(environments, environment)
	}
	return environments
}

// Ready reports whether the initial cache synchronization completed. Before
// readiness every health projection is unknown by contract.
func (s *Store) Ready() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ready
}

// Source projects the observation source state for module evaluation.
func (s *Store) Source() module.SourceStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return module.SourceStatus{State: s.state, StaleSince: s.staleSince, LastSync: s.lastSync}
}

// Snapshot is one environment's consistent view: leading source status plus
// every owned object in deterministic order.
type Snapshot struct {
	Source  module.SourceStatus
	Objects []Object
}

// ForService projects the module-facing resources of one service key:
// source first, then the service's own objects, then the shared platform
// objects (database pools) they reference.
func (s Snapshot) ForService(key string) []module.ObservedResource {
	resources := []module.ObservedResource{{
		Kind: module.KindSource, Name: "kubernetes", Source: &s.Source,
	}}
	shared := make(map[string]bool)
	for index := range s.Objects {
		obj := &s.Objects[index]
		if obj.Service != key {
			continue
		}
		switch obj.Kind {
		case module.KindWorkload, module.KindPod, module.KindAutoscaler,
			module.KindService, module.KindIngress, module.KindVolume,
			module.KindDatabaseClaim, module.KindDatabaseTenant:
			resources = append(resources, module.ObservedResource{
				Kind:           obj.Kind,
				Name:           obj.Name,
				Revision:       obj.Revision,
				Workload:       obj.Workload,
				Pod:            obj.Pod,
				Autoscaler:     obj.Autoscaler,
				Claim:          obj.Claim,
				DatabaseTenant: obj.DatabaseTenant,
			})
			if obj.SharedKey != "" {
				shared[obj.SharedKey] = true
			}
		}
	}
	for index := range s.Objects {
		obj := &s.Objects[index]
		if obj.Kind != module.KindDatabaseCluster || !shared[obj.SharedKey] {
			continue
		}
		resources = append(resources, module.ObservedResource{
			Kind:            obj.Kind,
			Name:            obj.Name,
			DatabaseCluster: obj.DatabaseCluster,
		})
	}
	return resources
}

// Snapshot copies the environment's objects under the read lock, plus the
// platform-scoped shared objects (database pools) its objects reference.
func (s *Store) Snapshot(environmentID uuid.UUID) Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snapshot := Snapshot{
		Source: module.SourceStatus{State: s.state, StaleSince: s.staleSince, LastSync: s.lastSync},
	}
	shared := make(map[string]bool)
	for key := range s.byEnvironment[environmentID] {
		if obj := s.objects[key]; obj != nil {
			snapshot.Objects = append(snapshot.Objects, *obj)
			if obj.SharedKey != "" && !shared[obj.SharedKey] {
				shared[obj.SharedKey] = true
				if sharedKey, ok := s.sharedObjects[obj.SharedKey]; ok {
					if sharedObj := s.objects[sharedKey]; sharedObj != nil {
						snapshot.Objects = append(snapshot.Objects, *sharedObj)
					}
				}
			}
		}
	}
	sort.Slice(snapshot.Objects, func(i, j int) bool {
		left, right := snapshot.Objects[i], snapshot.Objects[j]
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		if left.Ref.Namespace != right.Ref.Namespace {
			return left.Ref.Namespace < right.Ref.Namespace
		}
		return left.Ref.Name < right.Ref.Name
	})
	return snapshot
}

// EnvironmentsOnNode lists environments with pods placed on the node, for
// node-event fan-out.
func (s *Store) EnvironmentsOnNode(node string) []uuid.UUID {
	s.mu.RLock()
	defer s.mu.RUnlock()
	environments := make([]uuid.UUID, 0, len(s.podsByNode[node]))
	for environment := range s.podsByNode[node] {
		environments = append(environments, environment)
	}
	sort.Slice(environments, func(i, j int) bool {
		return environments[i].String() < environments[j].String()
	})
	return environments
}

// SetNodeArch records one node's CPU architecture. An empty arch removes
// the entry so a node that stops reporting does not pin a stale value.
func (s *Store) SetNodeArch(name, arch string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if arch == "" {
		delete(s.nodeArch, name)
		return
	}
	s.nodeArch[name] = arch
}

// EnvironmentsForSharedKey lists the environments referencing one shared
// platform object, for watch fan-out.
func (s *Store) EnvironmentsForSharedKey(sharedKey string) []uuid.UUID {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.environmentsForSharedKeyLocked(sharedKey)
}

// SetNodeCapabilities records one node's installed capability labels. An
// empty list removes the entry.
func (s *Store) SetNodeCapabilities(name string, capabilities []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(capabilities) == 0 {
		delete(s.nodeCapabilities, name)
		return
	}
	set := make(map[string]bool, len(capabilities))
	for _, capability := range capabilities {
		set[capability] = true
	}
	s.nodeCapabilities[name] = set
}

// CapableNodes lists the nodes carrying a capability, sorted by name. The
// substrate derives availability-tier satisfiability from the count.
func (s *Store) CapableNodes(capability string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var nodes []string
	for name, set := range s.nodeCapabilities {
		if set[capability] {
			nodes = append(nodes, name)
		}
	}
	sort.Strings(nodes)
	return nodes
}

// RemoveNode drops a deleted node's architecture and capability records.
func (s *Store) RemoveNode(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.nodeArch, name)
	delete(s.nodeCapabilities, name)
}

// NodePlatforms lists the platforms images must target to run on the
// observed nodes, as sorted deduplicated "linux/<arch>" strings. Empty
// until the node informer has delivered anything.
func (s *Store) NodePlatforms() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := make(map[string]struct{}, len(s.nodeArch))
	platforms := make([]string, 0, len(s.nodeArch))
	for _, arch := range s.nodeArch {
		platform := "linux/" + arch
		if _, ok := seen[platform]; ok {
			continue
		}
		seen[platform] = struct{}{}
		platforms = append(platforms, platform)
	}
	sort.Strings(platforms)
	return platforms
}

// ManagedNamespaces lists observed managed namespaces for audit
// diagnostics (orphan detection never deletes anything).
func (s *Store) ManagedNamespaces() []Object {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var namespaces []Object
	for _, obj := range s.objects {
		if obj.Kind == kindNamespace {
			namespaces = append(namespaces, *obj)
		}
	}
	sort.Slice(namespaces, func(i, j int) bool { return namespaces[i].Ref.Name < namespaces[j].Ref.Name })
	return namespaces
}

// EventRecord is one warning event joined to a managed object; a bounded
// ring per object, pruned by age at read and write time.
type EventRecord struct {
	Reason  string
	Message string
	Count   int32
	At      time.Time
}

const (
	maxEventsPerObject = 10
	maxEventAge        = 15 * time.Minute
)

// RecordEvent appends one warning event to the involved object's ring.
func (s *Store) RecordEvent(ref kube.ObjectRef, record EventRecord) {
	key := keyOf(ref)
	cutoff := s.clock().Add(-maxEventAge)
	s.mu.Lock()
	ring := s.events[key]
	kept := ring[:0]
	for _, existing := range ring {
		if existing.At.After(cutoff) {
			kept = append(kept, existing)
		}
	}
	kept = append(kept, record)
	if len(kept) > maxEventsPerObject {
		kept = kept[len(kept)-maxEventsPerObject:]
	}
	s.events[key] = kept
	s.mu.Unlock()
}

// Events reads the retained warning events of one object, newest last.
func (s *Store) Events(ref kube.ObjectRef) []EventRecord {
	cutoff := s.clock().Add(-maxEventAge)
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []EventRecord
	for _, record := range s.events[keyOf(ref)] {
		if record.At.After(cutoff) {
			out = append(out, record)
		}
	}
	return out
}

// Environment resolves the owning environment of an observed object, used
// by event fan-in where only the involved object reference is known.
func (s *Store) Environment(ref kube.ObjectRef) (uuid.UUID, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if obj := s.objects[keyOf(ref)]; obj != nil && obj.Environment != uuid.Nil {
		return obj.Environment, true
	}
	return uuid.Nil, false
}

// environments snapshots every environment currently holding objects.
func (s *Store) environments() []uuid.UUID {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]uuid.UUID, 0, len(s.byEnvironment))
	for environment := range s.byEnvironment {
		out = append(out, environment)
	}
	return out
}

func (s *Store) invalidate(environmentID uuid.UUID) {
	if environmentID != uuid.Nil {
		s.broadcast.publish(Invalidation{EnvironmentID: environmentID})
	}
}

// Subscribe delivers invalidations for one environment; the channel closes
// when the subscriber falls behind (resubscribe and re-read, same contract
// as the journal stream).
func (s *Store) Subscribe(environmentID uuid.UUID) (<-chan Invalidation, func()) {
	return s.broadcast.subscribe(environmentID)
}

// Invalidate nudges an environment's subscribers after a projection-relevant
// change outside the store itself (activation moved a pointer).
func (s *Store) Invalidate(environmentID uuid.UUID) {
	s.invalidate(environmentID)
}
