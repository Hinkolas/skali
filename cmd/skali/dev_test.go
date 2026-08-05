package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/client"
)

func TestIsDeploymentInFlight(t *testing.T) {
	inFlight := &client.APIError{Status: 409, Code: "deployment_in_flight", Message: "busy"}
	require.True(t, isDeploymentInFlight(inFlight))
	require.True(t, isDeploymentInFlight(fmt.Errorf("open: %w", inFlight)))
	require.False(t, isDeploymentInFlight(&client.APIError{Status: 409, Code: "conflict"}))
	require.False(t, isDeploymentInFlight(fmt.Errorf("plain")))
	require.False(t, isDeploymentInFlight(nil))
}

func TestIsRunAlreadyFinished(t *testing.T) {
	finished := &client.APIError{Status: 409, Code: "conflict", Message: "the run is already succeeded"}
	require.True(t, isRunAlreadyFinished(finished))
	require.True(t, isRunAlreadyFinished(fmt.Errorf("cancel: %w", finished)))
	require.False(t, isRunAlreadyFinished(&client.APIError{Status: 409, Code: "deployment_in_flight"}))
	require.False(t, isRunAlreadyFinished(&client.APIError{Status: 404, Code: "conflict"}))
	require.False(t, isRunAlreadyFinished(nil))
}

// fakeRuns is a minimal runs API for in-flight resolution tests. ListRuns
// serves the list snapshot; GetRun serves the (possibly newer) per-run
// status, so a run can be running in the list but already terminal when
// attached, which keeps tests free of polling waits. Cancels are recorded.
type fakeRuns struct {
	mu             sync.Mutex
	list           []client.Run
	byID           map[string]client.Run
	cancels        []string
	cancelConflict bool
	srv            *httptest.Server
}

func newFakeRuns(t *testing.T) *fakeRuns {
	t.Helper()
	f := &fakeRuns{byID: map[string]client.Run{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/environments/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"runs": f.list})
	})
	mux.HandleFunc("/v1/runs/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		id := strings.TrimPrefix(r.URL.Path, "/v1/runs/")
		if cancelled, ok := strings.CutSuffix(id, "/cancel"); ok {
			f.cancels = append(f.cancels, cancelled)
			if f.cancelConflict {
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{
					"code": "conflict", "message": "the run is already succeeded"}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "cancelled", "fallback": false})
			return
		}
		run, ok := f.byID[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{
				"code": "not_found", "message": "not found"}})
			return
		}
		_ = json.NewEncoder(w).Encode(client.RunTree{Run: run, Steps: []client.Step{}})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRuns) seed(list []client.Run, byID map[string]client.Run) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.list = list
	f.byID = byID
}

func (f *fakeRuns) client() *client.Client {
	return client.New(f.srv.URL, "token", "test")
}

func TestFindRunningRun(t *testing.T) {
	f := newFakeRuns(t)
	f.seed([]client.Run{
		{ID: "r3", Kind: "deployment", Status: "succeeded"},
		{ID: "r2", Kind: "deployment", Status: "running"},
		{ID: "r1", Kind: "teardown", Status: "failed"},
	}, nil)
	run, err := findRunningRun(context.Background(), f.client(), "e1")
	require.NoError(t, err)
	require.NotNil(t, run)
	require.Equal(t, "r2", run.ID)

	f.seed([]client.Run{
		{ID: "r2", Kind: "deployment", Status: "pending"},
		{ID: "r1", Kind: "deployment", Status: "succeeded"},
	}, nil)
	run, err = findRunningRun(context.Background(), f.client(), "e1")
	require.NoError(t, err)
	require.Nil(t, run)
}

func TestDevResolveInFlight(t *testing.T) {
	resolve := func(f *fakeRuns, force bool) (string, string, error) {
		var out strings.Builder
		action, err := devResolveInFlight(context.Background(), &out, f.client(), "e1", force)
		return action, out.String(), err
	}

	t.Run("NoRunningRunProceeds", func(t *testing.T) {
		f := newFakeRuns(t)
		f.seed([]client.Run{{ID: "r1", Kind: "deployment", Status: "succeeded"}}, nil)
		action, out, err := resolve(f, false)
		require.NoError(t, err)
		require.Equal(t, devInFlightProceed, action)
		require.Empty(t, out)
		require.Empty(t, f.cancels)
	})

	t.Run("ForceCancelsRunningRun", func(t *testing.T) {
		f := newFakeRuns(t)
		f.seed([]client.Run{{ID: "r1", Kind: "deployment", Status: "running"}}, nil)
		action, out, err := resolve(f, true)
		require.NoError(t, err)
		require.Equal(t, devInFlightProceed, action)
		require.Contains(t, out, "cancelling in-flight deployment run r1 (--force)")
		require.Equal(t, []string{"r1"}, f.cancels)
	})

	t.Run("ForceToleratesFinishedRun", func(t *testing.T) {
		f := newFakeRuns(t)
		f.seed([]client.Run{{ID: "r1", Kind: "deployment", Status: "running"}}, nil)
		f.cancelConflict = true
		action, _, err := resolve(f, true)
		require.NoError(t, err)
		require.Equal(t, devInFlightProceed, action)
	})

	t.Run("WaitsOutTeardownRun", func(t *testing.T) {
		f := newFakeRuns(t)
		f.seed([]client.Run{{ID: "r1", Kind: "teardown", Status: "running"}},
			map[string]client.Run{"r1": {ID: "r1", Kind: "teardown", Status: "succeeded"}})
		action, out, err := resolve(f, false)
		require.NoError(t, err)
		require.Equal(t, devInFlightProceed, action)
		require.Contains(t, out, "a teardown is in flight; waiting for run r1 to finish")
		require.Empty(t, f.cancels)
	})

	t.Run("AttachesToDeploymentRun", func(t *testing.T) {
		f := newFakeRuns(t)
		f.seed([]client.Run{{ID: "r1", Kind: "deployment", Status: "running"}},
			map[string]client.Run{"r1": {ID: "r1", Kind: "deployment", Status: "succeeded"}})
		action, out, err := resolve(f, false)
		require.NoError(t, err)
		require.Equal(t, devInFlightAttached, action)
		require.Contains(t, out, "a deployment is already in flight; attaching to run r1")
		require.Contains(t, out, "ready")
		require.Empty(t, f.cancels)
	})

	t.Run("FailedRunSurfaces", func(t *testing.T) {
		f := newFakeRuns(t)
		f.seed([]client.Run{{ID: "r1", Kind: "deployment", Status: "running"}},
			map[string]client.Run{"r1": {ID: "r1", Kind: "deployment", Status: "failed"}})
		_, _, err := resolve(f, false)
		require.EqualError(t, err, "run r1 failed")
	})
}

func TestAttachRunInterruptedByParentDeadline(t *testing.T) {
	// A parent context dying under the wait (the pause epilogue's deadline)
	// is not a user detach: attachRun must report "interrupted" and never
	// claim the user detached from a run they were still waiting on.
	f := newFakeRuns(t)
	f.seed(nil, map[string]client.Run{"r1": {ID: "r1", Kind: "teardown", Status: "running"}})
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	var out strings.Builder
	status, err := attachRun(ctx, &out, f.client(), "r1", "")
	require.NoError(t, err)
	require.Equal(t, "interrupted", status)
	require.NotContains(t, out.String(), "detached from run")
}
