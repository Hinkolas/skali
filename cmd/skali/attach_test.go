package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/client"
)

// stallingRuns serves one run and can hold requests open: with stallPolls
// every poll after the first waits, and with stallCancel the cancel
// request waits, until release or until the client gives up on it.
type stallingRuns struct {
	mu          sync.Mutex
	run         client.Run
	steps       []client.Step
	polls       int
	cancels     int
	continues   bool
	stallPolls  bool
	stallCancel bool

	gate    chan struct{}
	stalled chan string // the path of each request that starts waiting
	aborted chan string // the path of each waiting request the client dropped
	srv     *httptest.Server
}

func newStallingRuns(t *testing.T, run client.Run, steps ...client.Step) *stallingRuns {
	t.Helper()
	f := &stallingRuns{run: run, steps: steps, gate: make(chan struct{}),
		stalled: make(chan string, 16), aborted: make(chan string, 16)}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/runs/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/cancel") {
			f.mu.Lock()
			f.cancels++
			stall, continues := f.stallCancel, f.continues
			f.mu.Unlock()
			if stall && !f.wait(r) {
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "cancelled", "continues": continues})
			return
		}
		f.mu.Lock()
		f.polls++
		stall := f.stallPolls && f.polls > 1
		f.mu.Unlock()
		if stall && !f.wait(r) {
			return
		}
		f.mu.Lock()
		tree := client.RunTree{Run: f.run, Steps: f.steps}
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(tree)
	})
	mux.HandleFunc("/v1/steps/", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"logs": []any{}})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	t.Cleanup(f.release)
	return f
}

// wait holds a request until release; false means the client dropped it.
func (f *stallingRuns) wait(r *http.Request) bool {
	f.stalled <- r.URL.Path
	select {
	case <-f.gate:
		return true
	case <-r.Context().Done():
		f.aborted <- r.URL.Path
		return false
	}
}

func (f *stallingRuns) release() {
	f.mu.Lock()
	defer f.mu.Unlock()
	select {
	case <-f.gate:
	default:
		close(f.gate)
	}
}

func (f *stallingRuns) setStatus(status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.run.Status = status
}

func (f *stallingRuns) client() *client.Client {
	return client.New(f.srv.URL, "token", client.Caller{UserAgent: "test"})
}

func receiveWithin(t *testing.T, channel <-chan string, within time.Duration, what string) string {
	t.Helper()
	select {
	case value := <-channel:
		return value
	case <-time.After(within):
		t.Fatalf("timed out waiting for %s", what)
		return ""
	}
}

func TestPublishLatestKeepsOnlyTheNewestSnapshot(t *testing.T) {
	snapshots := make(chan runSnapshot, 1)
	for _, status := range []string{"pending", "running", "succeeded"} {
		publishLatest(snapshots, runSnapshot{tree: &client.RunTree{Run: client.Run{Status: status}}})
	}
	require.Equal(t, "succeeded", (<-snapshots).tree.Run.Status)
	require.Empty(t, snapshots)
}

func TestObserveRunEndsWithTheFinalSnapshot(t *testing.T) {
	f := newStallingRuns(t, client.Run{ID: "r1", Kind: "deployment", Status: "running"})
	snapshots := observeRun(context.Background(), f.client(), "r1", nil, false)
	first := <-snapshots
	require.NoError(t, first.err)
	require.Equal(t, "running", first.tree.Run.Status)
	f.setStatus("succeeded")
	var last runSnapshot
	for snapshot := range snapshots {
		last = snapshot
	}
	require.NoError(t, last.err)
	require.Equal(t, "succeeded", last.tree.Run.Status)
}

func TestObserveRunStopsWithItsContext(t *testing.T) {
	f := newStallingRuns(t, client.Run{ID: "r1", Kind: "deployment", Status: "running"})
	f.mu.Lock()
	f.stallPolls = true
	f.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	snapshots := observeRun(ctx, f.client(), "r1", nil, false)
	require.Equal(t, "running", (<-snapshots).tree.Run.Status)
	receiveWithin(t, f.stalled, 5*time.Second, "the second poll to stall")
	cancel()
	receiveWithin(t, f.aborted, 5*time.Second, "the stalled poll to be dropped")
	for snapshot := range snapshots {
		t.Fatalf("unexpected snapshot after cancellation: %+v", snapshot)
	}
}
