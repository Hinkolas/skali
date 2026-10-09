package observe

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
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

	// An identical probe changes no projection and enqueues nothing.
	source.pollOnce(context.Background())
	require.Equal(t, []uuid.UUID{envID}, enqueued)

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

	// A successful probe recovers, and wakes the environment although its
	// snapshot is identical: its passes waited on the source.
	fail = false
	source.pollOnce(context.Background())
	status, _ = store.SourceNamed("seaweedfs")
	require.Equal(t, module.SourceFresh, status.State)
	require.True(t, status.StaleSince.IsZero())
	require.Equal(t, []uuid.UUID{envID, envID}, enqueued)
}

func TestPollSourcePokeCoalesces(t *testing.T) {
	t.Parallel()
	source := NewPollSource(NewStore(nil), func(context.Context) ([]Object, error) { return nil, nil }, PollOptions{Source: "seaweedfs"})
	source.Poke()
	source.Poke()
	source.Poke()
	require.Len(t, source.poke, 1, "pokes coalesce into one pending re-poll")
}

// A failing probe says why: the first failure is logged with its error,
// a persisting one is not repeated every poll, and the recovery is
// logged once.
func TestPollSourceLogsFailures(t *testing.T) {
	t.Parallel()
	store := NewStore(nil)
	var fail error
	source := NewPollSource(store, func(context.Context) ([]Object, error) { return nil, fail }, PollOptions{
		Source: "seaweedfs", Interval: time.Hour,
	})
	var logs bytes.Buffer
	source.log = slog.New(slog.NewTextHandler(&logs, nil))
	store.RegisterSource("seaweedfs")

	source.pollOnce(context.Background())
	require.Empty(t, logs.String(), "a healthy probe logs nothing")

	fail = errors.New("context deadline exceeded")
	source.pollOnce(context.Background())
	source.pollOnce(context.Background())
	require.Equal(t, 1, strings.Count(logs.String(), "observe: probe failed"))
	require.Contains(t, logs.String(), "context deadline exceeded")
	require.Contains(t, logs.String(), "source=seaweedfs")

	source.lastLogged = time.Now().Add(-failureLogEvery)
	source.pollOnce(context.Background())
	require.Equal(t, 2, strings.Count(logs.String(), "observe: probe failed"), "a persisting failure is repeated once a minute")

	fail = nil
	source.pollOnce(context.Background())
	source.pollOnce(context.Background())
	require.Equal(t, 1, strings.Count(logs.String(), "observe: probe recovered"))
}
