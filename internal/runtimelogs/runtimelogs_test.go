package runtimelogs

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/testdb"
)

func TestStreamGuards(t *testing.T) {
	t.Parallel()
	var absent *Streamer
	require.ErrorIs(t, absent.Stream(context.Background(), uuid.New(), "", nil), ErrNoCluster)

	st := store.NewStore(testdb.New(t))
	streamer := &Streamer{Clientset: fake.NewSimpleClientset(), Observed: observe.NewFake().Store, Store: st}
	require.ErrorIs(t, streamer.Stream(context.Background(), uuid.New(), "", nil), ErrEnvironmentNotFound)
}

// The streamer discovers members from the observed snapshot and merges
// their log streams with member labels; the fake clientset answers every
// log request with a fixed line.
func TestStreamMergesMemberLogs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.NewStore(testdb.New(t))
	projects := project.New(st)
	proj, err := projects.Create(ctx, "demo", "", uuid.Nil)
	require.NoError(t, err)
	env, err := projects.CreateEnvironment(ctx, proj.ID, "production", project.EnvironmentOptions{})
	require.NoError(t, err)

	observed := observe.NewFake()
	observed.SetFresh()
	observed.SetPod(env.ID, "skali-demo-production", "web", "web-1", "node-a",
		module.PodStatus{Phase: "Running", Ready: true})
	observed.SetPod(env.ID, "skali-demo-production", "web", "web-2", "node-a",
		module.PodStatus{Phase: "Running", Ready: true})
	observed.SetPod(env.ID, "skali-demo-production", "worker", "worker-1", "node-a",
		module.PodStatus{Phase: "Running", Ready: true})

	streamer := &Streamer{Clientset: fake.NewSimpleClientset(), Observed: observed.Store, Store: st}
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	out := make(chan Event, 16)
	done := make(chan error, 1)
	go func() { done <- streamer.Stream(streamCtx, env.ID, "web", out) }()

	// Both web members deliver; the worker is filtered out.
	pods := map[string]bool{}
	deadline := time.After(5 * time.Second)
	for len(pods) < 2 {
		select {
		case event := <-out:
			require.Equal(t, "web", event.Service)
			require.Equal(t, "fake logs", event.Line)
			pods[event.Pod] = true
			require.NotEqual(t, "worker-1", event.Pod)
		case <-deadline:
			t.Fatalf("timed out; saw %v", pods)
		}
	}
	require.True(t, pods["web-1"] && pods["web-2"])

	cancel()
	require.NoError(t, <-done)
}
