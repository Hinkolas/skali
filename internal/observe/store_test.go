package observe

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Hinkolas/skali/internal/kube"
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
	require.Equal(t, module.SourceFresh, snapshot.Source.State)
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

func TestStalenessEvaluation(t *testing.T) {
	t.Parallel()
	now := time.Unix(1700000000, 0)
	clock := func() time.Time { return now }
	store := NewStore(clock)

	require.Equal(t, module.SourceUnknown, store.Source().State)
	require.False(t, store.Ready())

	store.MarkReady()
	require.True(t, store.Ready())
	require.Equal(t, module.SourceFresh, store.Source().State)

	// A failure alone does not flip the state; the threshold decides.
	store.MarkFailure()
	store.EvaluateFreshness(30 * time.Second)
	require.Equal(t, module.SourceFresh, store.Source().State)

	now = now.Add(31 * time.Second)
	store.EvaluateFreshness(30 * time.Second)
	source := store.Source()
	require.Equal(t, module.SourceStale, source.State)
	require.Equal(t, time.Unix(1700000000, 0), source.StaleSince)

	// A successful contact recovers.
	store.MarkContact()
	source = store.Source()
	require.Equal(t, module.SourceFresh, source.State)
	require.True(t, source.StaleSince.IsZero())

	// Shutdown returns to unknown, never straight to stale.
	store.MarkUnready()
	require.Equal(t, module.SourceUnknown, store.Source().State)
	require.False(t, store.Ready())
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
	// disconnected rather than blocking the write side.
	for range 200 {
		fake.SetWorkload(envID, "ns", "demo-web", "web", "abcd", module.WorkloadStatus{Desired: 1, Ready: 1})
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
