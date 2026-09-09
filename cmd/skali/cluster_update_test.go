package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/updates"
	"github.com/stretchr/testify/require"
)

func TestManagedCLIUsesAggregateAction(t *testing.T) {
	for _, action := range []string{"update", "finish", "retry"} {
		t.Run(action, func(t *testing.T) {
			var paths []string
			status := updates.Status{Installed: updates.Installed{Version: "v0.1.0-alpha.4", PlatformVersion: "v0.1.0-alpha.3"}, Managed: true, Manageable: true,
				Summary: updates.Summary{State: "incomplete", Action: action, TargetVersion: "v0.1.0-alpha.4", ConvergedVersion: "v0.1.0-alpha.3"}}
			if action == "update" {
				status.Summary.State = "available"
			}
			if action == "retry" {
				status.Summary.State = "failed"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.Method+" "+r.URL.Path)
				require.Equal(t, "Bearer token", r.Header.Get("Authorization"))
				if strings.HasSuffix(r.URL.Path, "/apply") {
					var request map[string]string
					require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
					require.Equal(t, "v0.1.0-alpha.4", request["version"])
				}
				if strings.HasSuffix(r.URL.Path, "/apply") || strings.HasSuffix(r.URL.Path, "/resume") {
					status.Operation = &updates.OperationState{ID: "op", Phase: "pending"}
				}
				w.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(w).Encode(status))
			}))
			defer server.Close()
			var out bytes.Buffer
			err := runManagedUpdate(context.Background(), &out, bufio.NewReader(strings.NewReader("")), client.New(server.URL, "token", "test"), "", true, false)
			require.NoError(t, err)
			if action == "update" {
				require.Contains(t, paths, "POST /v1/system/updates/scan")
			} else {
				require.NotContains(t, paths, "POST /v1/system/updates/scan")
			}
			if action == "retry" {
				require.Contains(t, paths, "POST /v1/system/updates/resume")
				require.NotContains(t, paths, "POST /v1/system/updates/apply")
			} else {
				require.Contains(t, paths, "POST /v1/system/updates/apply")
			}
			require.Contains(t, out.String(), "Work continues")
		})
	}
}

func TestManagedCLIPropagatesErrorsWithoutRecoveryFallback(t *testing.T) {
	for _, code := range []int{401, 403, 503} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "GET", r.Method)
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"error":{"code":"unavailable","message":"no"}}`))
		}))
		var out bytes.Buffer
		err := runManagedUpdate(context.Background(), &out, bufio.NewReader(strings.NewReader("")), client.New(server.URL, "token", "test"), "", true, false)
		require.Error(t, err)
		require.Empty(t, out.String())
		server.Close()
	}
}

func TestRecoveryRequiresExactVersionBeforeInspectingHost(t *testing.T) {
	var out bytes.Buffer
	err := runRecoveryUpdate(context.Background(), &out, bufio.NewReader(strings.NewReader("")), "", true, false)
	require.ErrorContains(t, err, "requires an exact --version")
}

func TestManagedCLIPrioritizesAcceptedTarget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "GET", r.Method, "must not submit another operation")
		_ = json.NewEncoder(w).Encode(updates.Status{Summary: updates.Summary{
			State: "updating", TargetVersion: "v0.1.0-alpha.4",
		}, Operation: &updates.OperationState{ID: "accepted", Phase: "pending"}})
	}))
	defer server.Close()
	var out bytes.Buffer
	err := runManagedUpdate(context.Background(), &out, bufio.NewReader(strings.NewReader("")), client.New(server.URL, "token", "test"), "v0.1.0-alpha.5", true, false)
	require.ErrorContains(t, err, "finish the running update")
}
