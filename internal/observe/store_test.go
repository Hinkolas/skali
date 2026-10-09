package observe

import (
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/module"
)

// lifecycle.Verify does not apply here: it checks that every state reaches
// a terminal state, and freshness is deliberately cyclical (a source can
// always recover). The structural invariants are asserted directly.
func TestFreshnessMachine(t *testing.T) {
	t.Parallel()
	require.Equal(t, module.SourceUnknown, Freshness.Initial())
	for _, state := range Freshness.States {
		require.True(t, Freshness.Valid(state))
		require.False(t, Freshness.Terminal(state), "freshness has no terminal state")
	}
	require.True(t, Freshness.Can(module.SourceUnknown, module.SourceFresh))
	require.True(t, Freshness.Can(module.SourceFresh, module.SourceStale))
	require.True(t, Freshness.Can(module.SourceStale, module.SourceFresh))
	require.False(t, Freshness.Can(module.SourceUnknown, module.SourceStale),
		"an unsynced source can never be stale, only unknown")
}

func TestSnapshotAndForService(t *testing.T) {
	t.Parallel()
	fake := NewFake()
	envID := uuid.New()
	fake.SetFresh()
	fake.SetWorkload(envID, "ns", "demo-web", "web", "abcd", module.WorkloadStatus{Desired: 2, Ready: 2})
	fake.SetPod(envID, "ns", "web", "web-1", "node-a", module.PodStatus{Phase: "Running", Ready: true})
	fake.SetPod(envID, "ns", "web", "web-2", "node-b", module.PodStatus{Phase: "Running", Ready: true})
	fake.SetWorkload(envID, "ns", "demo-worker", "worker", "abcd", module.WorkloadStatus{Desired: 1, Ready: 0})

	snapshot := fake.Snapshot(envID)
	require.Equal(t, SourceKubernetes, snapshot.Sources[0].Name)
	require.Equal(t, module.SourceFresh, snapshot.Sources[0].State)
	require.Len(t, snapshot.Objects, 4)

	web := snapshot.ForService("web")
	require.Equal(t, module.KindSource, web[0].Kind, "the source pseudo-resource always leads")
	kinds := map[string]int{}
	for _, resource := range web[1:] {
		kinds[resource.Kind]++
	}
	require.Equal(t, map[string]int{module.KindWorkload: 1, module.KindPod: 2}, kinds)

	// The other service's snapshot never leaks in.
	worker := fake.Snapshot(envID).ForService("worker")
	require.Len(t, worker, 2)
	require.Equal(t, "worker", worker[1].Name)

	// Unknown environments still get the source resource.
	empty := fake.Snapshot(uuid.New()).ForService("web")
	require.Len(t, empty, 1)
}

func TestRemoveAndNodeIndex(t *testing.T) {
	t.Parallel()
	fake := NewFake()
	envID := uuid.New()
	otherEnv := uuid.New()
	fake.SetPod(envID, "ns", "web", "web-1", "node-a", module.PodStatus{Phase: "Running"})
	fake.SetPod(otherEnv, "other", "api", "api-1", "node-a", module.PodStatus{Phase: "Running"})
	fake.SetPod(envID, "ns", "web", "web-2", "node-b", module.PodStatus{Phase: "Running"})

	environments := fake.EnvironmentsOnNode("node-a")
	require.Len(t, environments, 2)
	require.Contains(t, environments, envID)
	require.Contains(t, environments, otherEnv)

	fake.Remove(kube.ObjectRef{
		GVK:       schema.GroupVersionKind{Version: "v1", Kind: "Pod"},
		Namespace: "ns", Name: "web-1",
	})
	environments = fake.EnvironmentsOnNode("node-a")
	require.Equal(t, []uuid.UUID{otherEnv}, environments)
	require.Len(t, fake.Snapshot(envID).Objects, 1)
}

func TestNodePlatforms(t *testing.T) {
	t.Parallel()
	store := NewStore(nil)
	require.Empty(t, store.NodePlatforms())

	store.SetNodeArch("a", "arm64")
	store.SetNodeArch("b", "amd64")
	store.SetNodeArch("c", "amd64")
	require.Equal(t, []string{"linux/amd64", "linux/arm64"}, store.NodePlatforms())

	store.RemoveNode("a")
	require.Equal(t, []string{"linux/amd64"}, store.NodePlatforms())

	store.SetNodeArch("d", "")
	require.Equal(t, []string{"linux/amd64"}, store.NodePlatforms(),
		"a node without a reported arch must not register")

	store.SetNodeArch("b", "")
	store.RemoveNode("c")
	require.Empty(t, store.NodePlatforms())
}

func TestNodePlatformsApplicationCapabilityFilter(t *testing.T) {
	t.Parallel()
	store := NewStore(nil)

	store.SetNodeArch("db", "amd64")
	store.SetNodeArch("app", "arm64")
	require.Equal(t, []string{"linux/amd64", "linux/arm64"}, store.NodePlatforms(),
		"without capability labels every node counts")

	store.SetNodeCapabilities("db", []string{layout.CapabilityDatabase})
	store.SetNodeCapabilities("app", []string{layout.CapabilityApplication})
	require.Equal(t, []string{"linux/arm64"}, store.NodePlatforms(),
		"database-only nodes must not widen the build platforms")

	store.SetNodeCapabilities("db", []string{layout.CapabilityDatabase, layout.CapabilityApplication})
	require.Equal(t, []string{"linux/amd64", "linux/arm64"}, store.NodePlatforms())

	store.SetNodeCapabilities("db", nil)
	store.SetNodeCapabilities("app", nil)
	require.Equal(t, []string{"linux/amd64", "linux/arm64"}, store.NodePlatforms(),
		"losing every capability label falls back to all nodes")

	store.SetNodeCapabilities("app", []string{layout.CapabilityApplication})
	store.SetNodeArch("app", "")
	require.Empty(t, store.NodePlatforms(),
		"an application node without a reported arch leaves the platforms unknown")
}

func TestNodeRecords(t *testing.T) {
	t.Parallel()
	store := NewStore(nil)
	require.Empty(t, store.Nodes())

	store.SetNodeRecord(NodeRecord{Name: "b", Role: "agent", Arch: "amd64", Ready: true, Schedulable: true})
	store.SetNodeRecord(NodeRecord{Name: "a", Role: "server", Arch: "arm64", Ready: true, Schedulable: true})

	nodes := store.Nodes()
	require.Len(t, nodes, 2)
	require.Equal(t, "a", nodes[0].Name, "records sort by name")
	require.Equal(t, "server", nodes[0].Role)
	require.Equal(t, "b", nodes[1].Name)

	// An update replaces the record in place.
	store.SetNodeRecord(NodeRecord{Name: "b", Role: "agent", Arch: "amd64", Ready: false, Schedulable: false})
	nodes = store.Nodes()
	require.Len(t, nodes, 2)
	require.False(t, nodes[1].Ready)

	store.RemoveNode("a")
	nodes = store.Nodes()
	require.Len(t, nodes, 1)
	require.Equal(t, "b", nodes[0].Name)
}

func TestSmallestCapableNodeMemory(t *testing.T) {
	t.Parallel()
	store := NewStore(nil)
	name, bytes := store.SmallestCapableNodeMemory("database")
	require.Empty(t, name)
	require.Zero(t, bytes)

	// Capabilities and records arrive separately; a capable node without a
	// reported size does not count yet.
	store.SetNodeCapabilities("db-1", []string{"database"})
	store.SetNodeCapabilities("db-2", []string{"database"})
	store.SetNodeCapabilities("app-1", []string{"application"})
	name, bytes = store.SmallestCapableNodeMemory("database")
	require.Empty(t, name)
	require.Zero(t, bytes)

	store.SetNodeRecord(NodeRecord{Name: "app-1", MemoryAllocatableBytes: 1 << 30})
	store.SetNodeRecord(NodeRecord{Name: "db-2", MemoryAllocatableBytes: 16 << 30})
	name, bytes = store.SmallestCapableNodeMemory("database")
	require.Equal(t, "db-2", name, "the application node never counts")
	require.Equal(t, int64(16<<30), bytes)

	store.SetNodeRecord(NodeRecord{Name: "db-1", MemoryAllocatableBytes: 8 << 30})
	name, bytes = store.SmallestCapableNodeMemory("database")
	require.Equal(t, "db-1", name, "the smallest capable node bounds the pools")
	require.Equal(t, int64(8<<30), bytes)

	// Ties break by name.
	store.SetNodeRecord(NodeRecord{Name: "db-2", MemoryAllocatableBytes: 8 << 30})
	name, _ = store.SmallestCapableNodeMemory("database")
	require.Equal(t, "db-1", name)

	store.RemoveNode("db-1")
	name, _ = store.SmallestCapableNodeMemory("database")
	require.Equal(t, "db-2", name)
}

func TestSharedObject(t *testing.T) {
	t.Parallel()
	store := NewStore(nil)
	_, ok := store.SharedObject("pg17-shared")
	require.False(t, ok)

	ref := kube.ObjectRef{GVK: schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"},
		Namespace: "skali-platform", Name: "pg17-shared"}
	store.Upsert(Object{Ref: ref, Kind: module.KindDatabaseCluster, Name: "pg17-shared", SharedKey: "pg17-shared",
		DatabaseCluster: &module.DatabaseClusterStatus{Instances: 2, ReadyInstances: 1, Phase: "Upgrading cluster"}})
	obj, ok := store.SharedObject("pg17-shared")
	require.True(t, ok)
	require.Equal(t, "Upgrading cluster", obj.DatabaseCluster.Phase)

	// Environment-owned objects carrying the shared key are references, not
	// the shared object itself.
	_, ok = store.SharedObject("other")
	require.False(t, ok)

	store.Remove(ref)
	_, ok = store.SharedObject("pg17-shared")
	require.False(t, ok)
}

// A system tenant on a shared pool (the object store's metadata database)
// is platform-scoped and labelled with the pool like every tenant. It must
// not take the pool's place: environments on the pool would lose their pool
// observation, and read as waiting for it, every time the tenant updated.
func TestSystemTenantDoesNotReplaceItsPool(t *testing.T) {
	t.Parallel()
	store := NewStore(nil)
	environment := uuid.New()
	cluster := kube.ObjectRef{GVK: schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"},
		Namespace: "skali-platform", Name: "pg17-shared"}
	database := schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Database"}
	store.Upsert(Object{Ref: cluster, Kind: module.KindDatabaseCluster, Name: "pg17-shared", SharedKey: "pg17-shared",
		DatabaseCluster: &module.DatabaseClusterStatus{Instances: 2, ReadyInstances: 2}})
	store.Upsert(Object{Ref: kube.ObjectRef{GVK: database, Namespace: "skali-platform", Name: "db-app"},
		Kind: module.KindDatabaseTenant, Name: "app", Environment: environment, Service: "databases.data",
		SharedKey: "pg17-shared", DatabaseTenant: &module.DatabaseTenantStatus{Applied: true, Pool: "pg17-shared"}})
	system := kube.ObjectRef{GVK: database, Namespace: "skali-platform", Name: "db-system"}
	store.Upsert(Object{Ref: system, Kind: module.KindDatabaseTenant, Name: "seaweed",
		SharedKey: "pg17-shared", DatabaseTenant: &module.DatabaseTenantStatus{Applied: true, Pool: "pg17-shared"}})

	obj, ok := store.SharedObject("pg17-shared")
	require.True(t, ok)
	require.Equal(t, module.KindDatabaseCluster, obj.Kind, "the pool keeps its key after the system tenant updated")
	pools := 0
	for _, resource := range store.Snapshot(environment).ForService("databases.data") {
		if resource.Kind == module.KindDatabaseCluster {
			pools++
		}
	}
	require.Equal(t, 1, pools, "the environment's snapshot carries the pool")

	store.Remove(system)
	_, ok = store.SharedObject("pg17-shared")
	require.True(t, ok, "removing the system tenant leaves the pool in place")
}

func TestStalenessEvaluation(t *testing.T) {
	t.Parallel()
	now := time.Unix(1700000000, 0)
	clock := func() time.Time { return now }
	store := NewStore(clock)

	require.Equal(t, module.SourceUnknown, store.Source().State)
	require.False(t, store.Ready())

	store.MarkReady(SourceKubernetes)
	require.True(t, store.Ready())
	require.Equal(t, module.SourceFresh, store.Source().State)

	// A failure alone does not flip the state; the threshold decides.
	store.MarkFailure(SourceKubernetes)
	store.EvaluateFreshness(SourceKubernetes, 30*time.Second)
	require.Equal(t, module.SourceFresh, store.Source().State)

	now = now.Add(31 * time.Second)
	store.EvaluateFreshness(SourceKubernetes, 30*time.Second)
	source := store.Source()
	require.Equal(t, module.SourceStale, source.State)
	require.Equal(t, time.Unix(1700000000, 0), source.StaleSince)

	// A successful contact recovers.
	store.MarkContact(SourceKubernetes)
	source = store.Source()
	require.Equal(t, module.SourceFresh, source.State)
	require.True(t, source.StaleSince.IsZero())

	// Shutdown returns to unknown, never straight to stale.
	store.MarkUnready(SourceKubernetes)
	require.Equal(t, module.SourceUnknown, store.Source().State)
	require.False(t, store.Ready())
}

func TestPerSourceFreshnessIsolation(t *testing.T) {
	t.Parallel()
	now := time.Unix(1700000000, 0)
	clock := func() time.Time { return now }
	store := NewStore(clock)
	store.RegisterSource("seaweedfs")
	store.MarkReady(SourceKubernetes)
	store.MarkReady("seaweedfs")

	// A provider failure turns only the provider stale; the cluster view
	// keeps its meaning.
	store.MarkFailure("seaweedfs")
	now = now.Add(time.Minute)
	store.EvaluateFreshness("seaweedfs", 30*time.Second)
	require.Equal(t, module.SourceFresh, store.Source().State)
	seaweed, ok := store.SourceNamed("seaweedfs")
	require.True(t, ok)
	require.Equal(t, module.SourceStale, seaweed.State)

	// Listings and snapshots order kubernetes first so StaleSource keeps
	// examining cluster freshness.
	sources := store.Sources()
	require.Equal(t, SourceKubernetes, sources[0].Name)
	require.Equal(t, "seaweedfs", sources[1].Name)
	resources := store.Snapshot(uuid.New()).ForService("web")
	require.Len(t, resources, 2)
	require.Nil(t, module.StaleSource(resources))
	require.Equal(t, module.SourceStale, module.SourceNamed(resources, "seaweedfs").State)

	_, ok = store.SourceNamed("registry")
	require.False(t, ok)
}

func TestReplaceSourceReconcilesObjectSet(t *testing.T) {
	t.Parallel()
	store := NewStore(nil)
	envA, envB := uuid.New(), uuid.New()
	ref := func(name string) kube.ObjectRef {
		return kube.ObjectRef{
			GVK:  schema.GroupVersionKind{Group: "seaweed.skali.dev", Version: "v1", Kind: "Bucket"},
			Name: name,
		}
	}
	bucket := func(name string, env uuid.UUID) Object {
		return Object{
			Ref: ref(name), Kind: module.KindBucket, Name: "buckets.files",
			Environment: env, Service: "buckets.files",
			Bucket: &module.BucketStatus{Exists: true},
		}
	}

	affected := store.ReplaceSource("seaweedfs", []Object{bucket("b-a", envA), bucket("b-b", envB)})
	require.Len(t, affected, 2)
	require.Len(t, store.Snapshot(envA).Objects, 1)
	require.Len(t, store.Snapshot(envB).Objects, 1)

	// An unchanged probe reports nothing and leaves the set intact.
	affected = store.ReplaceSource("seaweedfs", []Object{bucket("b-a", envA), bucket("b-b", envB)})
	require.Empty(t, affected)
	require.Len(t, store.Snapshot(envB).Objects, 1)

	// A vanished object is removed: no ghosts after a provider crash.
	affected = store.ReplaceSource("seaweedfs", []Object{bucket("b-a", envA)})
	require.Contains(t, affected, envB)
	require.Empty(t, store.Snapshot(envB).Objects)
	require.Len(t, store.Snapshot(envA).Objects, 1)

	// Another source's objects are never touched by the reconcile.
	store.Upsert(Object{
		Ref:         kube.ObjectRef{GVK: schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, Namespace: "ns", Name: "web"},
		Kind:        module.KindWorkload,
		Name:        "web",
		Environment: envB,
		Service:     "web",
	})
	store.ReplaceSource("seaweedfs", nil)
	require.Len(t, store.Snapshot(envB).Objects, 1, "kubernetes objects survive a provider reconcile")
	require.Empty(t, store.Snapshot(envA).Objects)
}

// A probe reports exactly the environments whose projections changed in a
// way a pass can act on; subscribers still see usage move.
func TestReplaceSourceReportsOnlyChanges(t *testing.T) {
	t.Parallel()
	store := NewStore(nil)
	envA, envB := uuid.New(), uuid.New()
	storeRef := kube.ObjectRef{GVK: schema.GroupVersionKind{Group: "seaweed.skali.dev", Version: "v1", Kind: "ObjectStore"}, Name: "store"}
	objectStore := func(s3Ready bool) Object {
		return Object{Ref: storeRef, Kind: module.KindObjectStore, Name: "object-store", SharedKey: "object-store",
			ObjectStore: &module.ObjectStoreStatus{MastersDesired: 3, MastersReady: 3, S3Ready: s3Ready}}
	}
	bucket := func(env uuid.UUID, status module.BucketStatus) Object {
		return Object{
			Ref:  kube.ObjectRef{GVK: schema.GroupVersionKind{Group: "seaweed.skali.dev", Version: "v1", Kind: "Bucket"}, Name: "b-files"},
			Kind: module.KindBucket, Name: "buckets.files", Environment: env, Service: "buckets.files",
			SharedKey: "object-store", Bucket: &status,
		}
	}
	probe := func(store Object, buckets ...Object) []Object { return append([]Object{store}, buckets...) }
	events, cancel := store.Subscribe(envA)
	defer cancel()
	drain := func() int {
		for n := 0; ; n++ {
			select {
			case <-events:
			default:
				return n
			}
		}
	}
	idle := module.BucketStatus{Exists: true, UsedBytes: 10, QuotaBytes: 100}

	require.Equal(t, []uuid.UUID{envA}, store.ReplaceSource("seaweedfs", probe(objectStore(true), bucket(envA, idle))))
	drain()

	// Identical: no pass, no refresh.
	require.Empty(t, store.ReplaceSource("seaweedfs", probe(objectStore(true), bucket(envA, idle))))
	require.Zero(t, drain())

	// Usage alone: subscribers refresh, no pass is due.
	written := idle
	written.UsedBytes, written.DiskBytes, written.EntryCount = 20, 30, 2
	require.Empty(t, store.ReplaceSource("seaweedfs", probe(objectStore(true), bucket(envA, written))))
	require.Equal(t, 1, drain())

	// A nested status change that health reads is a change.
	full := written
	full.ReadOnly = true
	require.Equal(t, []uuid.UUID{envA}, store.ReplaceSource("seaweedfs", probe(objectStore(true), bucket(envA, full))))

	// The shared object store fans out to every environment referencing it,
	// but only when it changed.
	require.Equal(t, []uuid.UUID{envA}, store.ReplaceSource("seaweedfs", probe(objectStore(false), bucket(envA, full))))
	require.Empty(t, store.ReplaceSource("seaweedfs", probe(objectStore(false), bucket(envA, full))))

	// An owner move affects both the old and the new owner.
	want := []uuid.UUID{envA, envB}
	sort.Slice(want, func(i, j int) bool { return want[i].String() < want[j].String() })
	require.Equal(t, want, store.ReplaceSource("seaweedfs", probe(objectStore(false), bucket(envB, full))))

	// A removal affects the owner it leaves.
	require.Equal(t, []uuid.UUID{envB}, store.ReplaceSource("seaweedfs", probe(objectStore(false))))
}

func TestSubscribeInvalidations(t *testing.T) {
	t.Parallel()
	fake := NewFake()
	envID := uuid.New()
	events, cancel := fake.Subscribe(envID)
	defer cancel()

	fake.SetWorkload(envID, "ns", "demo-web", "web", "abcd", module.WorkloadStatus{Desired: 1, Ready: 1})
	select {
	case invalidation := <-events:
		require.Equal(t, envID, invalidation.EnvironmentID)
	case <-time.After(time.Second):
		t.Fatal("no invalidation delivered")
	}

	// Another environment's change never reaches this subscriber.
	fake.SetWorkload(uuid.New(), "other", "demo-api", "api", "abcd", module.WorkloadStatus{Desired: 1, Ready: 1})
	select {
	case invalidation, ok := <-events:
		require.False(t, ok, "unexpected invalidation %v", invalidation)
	default:
	}
}

func TestSubscribeOverflowCloses(t *testing.T) {
	t.Parallel()
	fake := NewFake()
	envID := uuid.New()
	events, cancel := fake.Subscribe(envID)
	defer cancel()

	// Never reading, the buffered channel fills and the subscriber is
	// disconnected rather than blocking the write side. Each write is a
	// real change: an identical one publishes nothing.
	for i := range 200 {
		fake.SetWorkload(envID, "ns", "demo-web", "web", "abcd", module.WorkloadStatus{Desired: 1, Ready: int32(i % 2)})
	}
	closed := false
	for !closed {
		select {
		case _, ok := <-events:
			if !ok {
				closed = true
			}
		case <-time.After(time.Second):
			t.Fatal("subscriber was not disconnected on overflow")
		}
	}
}

func TestEventRing(t *testing.T) {
	t.Parallel()
	now := time.Unix(1700000000, 0)
	store := NewStore(func() time.Time { return now })
	ref := kube.ObjectRef{GVK: schema.GroupVersionKind{Version: "v1", Kind: "Pod"}, Namespace: "ns", Name: "web-1"}

	for i := range 15 {
		store.RecordEvent(ref, EventRecord{Reason: "BackOff", Count: int32(i), At: now})
	}
	events := store.Events(ref)
	require.Len(t, events, maxEventsPerObject)
	require.Equal(t, int32(14), events[len(events)-1].Count)

	// Aged-out records disappear.
	now = now.Add(16 * time.Minute)
	require.Empty(t, store.Events(ref))
}
