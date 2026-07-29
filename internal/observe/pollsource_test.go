package observe

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/module"
)

func TestPollSourceLifecycle(t *testing.T) {
	t.Parallel()
	now := time.Unix(1700000000, 0)
	store := NewStore(func() time.Time { return now })
	envID := uuid.New()

	fail := false
	var enqueued []uuid.UUID
	probe := func(ctx context.Context) ([]Object, error) {
		if fail {
			return nil, errors.New("master unreachable")
		}
		return []Object{{
			Ref: kube.ObjectRef{
				GVK:  schema.GroupVersionKind{Group: "seaweed.skali.dev", Version: "v1", Kind: "Bucket"},
				Name: "b-files",
			},
			Kind: module.KindBucket, Name: "buckets.files",
			Environment: envID, Service: "buckets.files",
			Bucket: &module.BucketStatus{Exists: true},
		}}, nil
	}
	source := NewPollSource(store, probe, PollOptions{
		Source:         "seaweedfs",
		Interval:       time.Hour,
		StaleThreshold: 30 * time.Second,
		Enqueue:        func(id uuid.UUID) { enqueued = append(enqueued, id) },
	})
	store.RegisterSource("seaweedfs")

	// The first complete probe is the initial sync: ready, fresh, enqueued.
	source.pollOnce(context.Background())
	require.True(t, store.SourceReady("seaweedfs"))
	status, ok := store.SourceNamed("seaweedfs")
	require.True(t, ok)
	require.Equal(t, module.SourceFresh, status.State)
	require.Equal(t, []uuid.UUID{envID}, enqueued)
	require.Len(t, store.Snapshot(envID).Objects, 1)

	// A failure inside the threshold keeps the source fresh.
	fail = true
	source.pollOnce(context.Background())
	status, _ = store.SourceNamed("seaweedfs")
	require.Equal(t, module.SourceFresh, status.State)

	// Past the threshold with no recovery the source turns stale, and its
	// last known objects remain visible (stale, not vanished).
	now = now.Add(31 * time.Second)
	source.pollOnce(context.Background())
	status, _ = store.SourceNamed("seaweedfs")
	require.Equal(t, module.SourceStale, status.State)
	require.Len(t, store.Snapshot(envID).Objects, 1)

	// The kubernetes source never noticed any of it.
	require.Equal(t, module.SourceUnknown, store.Source().State)

	// A successful probe recovers.
	fail = false
	source.pollOnce(context.Background())
	status, _ = store.SourceNamed("seaweedfs")
	require.Equal(t, module.SourceFresh, status.State)
	require.True(t, status.StaleSince.IsZero())
}

func TestPollSourcePokeCoalesces(t *testing.T) {
	t.Parallel()
	source := NewPollSource(NewStore(nil), func(context.Context) ([]Object, error) { return nil, nil }, PollOptions{Source: "seaweedfs"})
	source.Poke()
	source.Poke()
	source.Poke()
	require.Len(t, source.poke, 1, "pokes coalesce into one pending re-poll")
}
