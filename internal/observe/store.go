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

	"github.com/Hinkolas/skali/internal/broadcast"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/module"
)

// Object is one observed cluster object in projected form. Kind uses the
// module vocabulary plus the store-internal kinds "namespace" and "node"
// that never reach module evaluation.
type Object struct {
	Ref         kube.ObjectRef
	Kind        string
	Name        string // module-facing name: service key for workloads, object name otherwise
	Labels      map[string]string
	Environment uuid.UUID // uuid.Nil when the object is not environment-owned
	Service     string
	Revision    string
	Node        string // pods only
	Generation  int64

	// ManagedFields are retained for workload objects so the scale planner
	// can decide ownership transitions without a request-time read.
	ManagedFields []metav1.ManagedFieldsEntry

	Workload   *module.WorkloadStatus
	Pod        *module.PodStatus
	Autoscaler *module.AutoscalerStatus
	Claim      *module.ClaimStatus
	Job        *JobStatus

	// SharedKey links platform-scoped objects (Environment == uuid.Nil,
	// e.g. a database pool) to the environment-owned objects that reference
	// them. Snapshots include the shared objects their environment
	// references, and shared-object changes fan out to every referencing
	// environment.
	SharedKey       string
	DatabaseCluster *module.DatabaseClusterStatus
	DatabaseTenant  *module.DatabaseTenantStatus

	// Source names the observation source that owns this object; empty means
	// the kubernetes watch. Poll-based provider sources set their name so
	// ReplaceSource can reconcile exactly their object set.
	Source      string
	ObjectStore *module.ObjectStoreStatus
	Bucket      *module.BucketStatus
}

// KindReleaseJob projects release-command Jobs for the reconciler's release
// gate. Like the store-internal kinds it never reaches module evaluation:
// Snapshot.ForService whitelists the module vocabulary.
const KindReleaseJob = "release-job"

// JobStatus projects one release Job's progress toward its terminal state.
type JobStatus struct {
	Succeeded bool
	Failed    bool
	Reason    string // terminal failure reason, e.g. BackoffLimitExceeded, DeadlineExceeded
	Message   string
	Created   time.Time
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
	nodeRecords      map[string]NodeRecord
	events           map[objectKey][]EventRecord
	sharedObjects    map[string]objectKey              // shared key -> platform object
	sharedRefs       map[string]map[uuid.UUID]int      // shared key -> referencing environments
	bySource         map[string]map[objectKey]struct{} // named provider sources only

	// sources is the per-source freshness registry; SourceKubernetes always
	// exists, provider observers register themselves. Sources fail
	// independently: one provider's staleness never poisons another's
	// projections.
	sources map[string]*sourceRecord

	broadcast *broadcast.Broadcaster[Invalidation]
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
		nodeRecords:      make(map[string]NodeRecord),
		events:           make(map[objectKey][]EventRecord),
		sharedObjects:    make(map[string]objectKey),
		sharedRefs:       make(map[string]map[uuid.UUID]int),
		bySource:         make(map[string]map[objectKey]struct{}),
		sources: map[string]*sourceRecord{
			SourceKubernetes: {state: module.SourceUnknown},
		},
		broadcast: broadcast.New[Invalidation](64),
	}
}

// Upsert records one observed object and publishes an invalidation for its
// environment; a platform-scoped shared object fans out to every
// environment referencing it. The write side is a watch source, a provider
// observer, or a test fake.
func (s *Store) Upsert(obj Object) {
	s.mu.Lock()
	affected := s.upsertLocked(obj)
	s.mu.Unlock()
	for _, environment := range affected {
		s.invalidate(environment)
	}
}

// upsertLocked stores one object and returns the environments whose
// projections changed (owner, previous owner, shared-key referencers).
func (s *Store) upsertLocked(obj Object) []uuid.UUID {
	key := keyOf(obj.Ref)
	previous := s.objects[key]
	if previous != nil {
		s.unindexLocked(key, previous)
	}
	stored := obj
	s.objects[key] = &stored
	s.indexLocked(key, &stored)
	affected := []uuid.UUID{obj.Environment}
	if previous != nil && previous.Environment != obj.Environment {
		affected = append(affected, previous.Environment)
	}
	if obj.Environment == uuid.Nil && obj.SharedKey != "" {
		affected = append(affected, s.environmentsForSharedKeyLocked(obj.SharedKey)...)
	}
	return affected
}

// Remove drops one observed object, if present.
func (s *Store) Remove(ref kube.ObjectRef) {
	s.mu.Lock()
	affected := s.removeLocked(keyOf(ref))
	s.mu.Unlock()
	for _, environment := range affected {
		s.invalidate(environment)
	}
}

// removeLocked drops one object and returns the affected environments.
func (s *Store) removeLocked(key objectKey) []uuid.UUID {
	previous := s.objects[key]
	if previous == nil {
		return nil
	}
	s.unindexLocked(key, previous)
	delete(s.objects, key)
	affected := []uuid.UUID{previous.Environment}
	if previous.Environment == uuid.Nil && previous.SharedKey != "" {
		affected = append(affected, s.environmentsForSharedKeyLocked(previous.SharedKey)...)
	}
	return affected
}

// ReplaceSource reconciles one named source's object set to exactly the
// given objects: upserts them all and removes anything the source published
// earlier that vanished from this probe, so a crashed provider leaves no
// ghosts. It returns the affected environments, deduplicated and sorted,
// for the caller to enqueue.
func (s *Store) ReplaceSource(source string, objects []Object) []uuid.UUID {
	incoming := make(map[objectKey]struct{}, len(objects))
	var affected []uuid.UUID
	s.mu.Lock()
	for _, obj := range objects {
		obj.Source = source
		incoming[keyOf(obj.Ref)] = struct{}{}
		affected = append(affected, s.upsertLocked(obj)...)
	}
	for key := range s.bySource[source] {
		if _, ok := incoming[key]; ok {
			continue
		}
		affected = append(affected, s.removeLocked(key)...)
	}
	s.mu.Unlock()

	seen := make(map[uuid.UUID]struct{}, len(affected))
	environments := make([]uuid.UUID, 0, len(affected))
	for _, environment := range affected {
		if environment == uuid.Nil {
			continue
		}
		if _, ok := seen[environment]; ok {
			continue
		}
		seen[environment] = struct{}{}
		environments = append(environments, environment)
	}
	sort.Slice(environments, func(i, j int) bool {
		return environments[i].String() < environments[j].String()
	})
	for _, environment := range environments {
		s.invalidate(environment)
	}
	return environments
}

func (s *Store) indexLocked(key objectKey, obj *Object) {
	if obj.Source != "" {
		if s.bySource[obj.Source] == nil {
			s.bySource[obj.Source] = make(map[objectKey]struct{})
		}
		s.bySource[obj.Source][key] = struct{}{}
	}
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
	if obj.Source != "" {
		if set := s.bySource[obj.Source]; set != nil {
			delete(set, key)
			if len(set) == 0 {
				delete(s.bySource, obj.Source)
			}
		}
	}
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

// Ready reports whether the kubernetes source's initial cache
// synchronization completed. Before readiness every health projection is
// unknown by contract. Provider sources have their own readiness, visible
// through Sources.
func (s *Store) Ready() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sources[SourceKubernetes].ready
}

// Source projects the kubernetes observation source for module evaluation
// and the kernel's activation gate; provider sources are read by name.
func (s *Store) Source() module.SourceStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sources[SourceKubernetes].status()
}

// SourceNamed projects one named source's status; ok is false when the
// source is not registered.
func (s *Store) SourceNamed(name string) (module.SourceStatus, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.sources[name]
	if !ok {
		return module.SourceStatus{}, false
	}
	return record.status(), true
}

// NamedSource is one source's status with its name, kubernetes first in
// every listing so consumers reading only the leading source keep their
// meaning.
type NamedSource struct {
	Name string
	module.SourceStatus
}

func (r *sourceRecord) status() module.SourceStatus {
	return module.SourceStatus{State: r.state, StaleSince: r.staleSince, LastSync: r.lastSync}
}

// sourcesLocked lists every registered source, kubernetes first, then
// sorted by name.
func (s *Store) sourcesLocked() []NamedSource {
	out := []NamedSource{{Name: SourceKubernetes, SourceStatus: s.sources[SourceKubernetes].status()}}
	names := make([]string, 0, len(s.sources))
	for name := range s.sources {
		if name != SourceKubernetes {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		out = append(out, NamedSource{Name: name, SourceStatus: s.sources[name].status()})
	}
	return out
}

// Sources lists every registered source's status, kubernetes first.
func (s *Store) Sources() []NamedSource {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sourcesLocked()
}

// Snapshot is one environment's consistent view: every source's status
// (kubernetes first) plus every owned object in deterministic order.
type Snapshot struct {
	Sources []NamedSource
	Objects []Object
}

// ForService projects the module-facing resources of one service key: the
// sources first (kubernetes leading, so StaleSource keeps its meaning),
// then the service's own objects, then the shared platform objects
// (database pools, the object store) they reference.
func (s Snapshot) ForService(key string) []module.ObservedResource {
	resources := make([]module.ObservedResource, 0, len(s.Sources))
	for index := range s.Sources {
		resources = append(resources, module.ObservedResource{
			Kind: module.KindSource, Name: s.Sources[index].Name, Source: &s.Sources[index].SourceStatus,
		})
	}
	shared := make(map[string]bool)
	for index := range s.Objects {
		obj := &s.Objects[index]
		if obj.Service != key {
			continue
		}
		switch obj.Kind {
		case module.KindWorkload, module.KindPod, module.KindAutoscaler,
			module.KindService, module.KindIngress, module.KindVolume,
			module.KindDatabaseClaim, module.KindDatabaseTenant,
			module.KindBucketClaim, module.KindBucket:
			resources = append(resources, module.ObservedResource{
				Kind:           obj.Kind,
				Name:           obj.Name,
				Revision:       obj.Revision,
				Workload:       obj.Workload,
				Pod:            obj.Pod,
				Autoscaler:     obj.Autoscaler,
				Claim:          obj.Claim,
				DatabaseTenant: obj.DatabaseTenant,
				Bucket:         obj.Bucket,
			})
			if obj.SharedKey != "" {
				shared[obj.SharedKey] = true
			}
		}
	}
	for index := range s.Objects {
		obj := &s.Objects[index]
		if obj.Environment != uuid.Nil || !shared[obj.SharedKey] {
			continue
		}
		switch obj.Kind {
		case module.KindDatabaseCluster, module.KindObjectStore:
			resources = append(resources, module.ObservedResource{
				Kind:            obj.Kind,
				Name:            obj.Name,
				DatabaseCluster: obj.DatabaseCluster,
				ObjectStore:     obj.ObjectStore,
			})
		}
	}
	return resources
}

// Snapshot copies the environment's objects under the read lock, plus the
// platform-scoped shared objects (database pools, the object store) its
// objects reference.
func (s *Store) Snapshot(environmentID uuid.UUID) Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snapshot := Snapshot{
		Sources: s.sourcesLocked(),
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

// RemoveNode drops a deleted node's architecture, capability, and record
// entries.
func (s *Store) RemoveNode(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.nodeArch, name)
	delete(s.nodeCapabilities, name)
	delete(s.nodeRecords, name)
}

// NodeRecord is the member-visible projection of one cluster node, fed by
// the node informer and served on /v1/nodes. It intentionally carries only
// kube-observed facts; per-node agent versions live with the installer, not
// here.
type NodeRecord struct {
	Name           string
	Role           string // layout.RoleServer or layout.RoleAgent
	Capabilities   []string
	Arch           string
	OS             string
	KubeletVersion string
	Ready          bool
	Schedulable    bool
	InternalIP     string
	ExternalIP     string
	LastHeartbeat  time.Time
}

// SetNodeRecord stores one node's projection, keyed by name.
func (s *Store) SetNodeRecord(record NodeRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodeRecords[record.Name] = record
}

// Nodes lists the observed node records sorted by name.
func (s *Store) Nodes() []NodeRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	nodes := make([]NodeRecord, 0, len(s.nodeRecords))
	for _, record := range s.nodeRecords {
		nodes = append(nodes, record)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	return nodes
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
		s.broadcast.Publish(environmentID, Invalidation{EnvironmentID: environmentID})
	}
}

// Subscribe delivers invalidations for one environment; the channel closes
// when the subscriber falls behind (resubscribe and re-read, same contract
// as the journal stream).
func (s *Store) Subscribe(environmentID uuid.UUID) (<-chan Invalidation, func()) {
	return s.broadcast.Subscribe(environmentID)
}

// Invalidate nudges an environment's subscribers after a projection-relevant
// change outside the store itself (activation moved a pointer).
func (s *Store) Invalidate(environmentID uuid.UUID) {
	s.invalidate(environmentID)
}
