package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"github.com/Hinkolas/skali/internal/cliconfig"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
			err := runManagedUpdate(context.Background(), &out, bufio.NewReader(strings.NewReader("")), client.New(server.URL, "token", client.Caller{UserAgent: "test"}), "", true, false)
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
		err := runManagedUpdate(context.Background(), &out, bufio.NewReader(strings.NewReader("")), client.New(server.URL, "token", client.Caller{UserAgent: "test"}), "", true, false)
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
	err := runManagedUpdate(context.Background(), &out, bufio.NewReader(strings.NewReader("")), client.New(server.URL, "token", client.Caller{UserAgent: "test"}), "v0.1.0-alpha.5", true, false)
	require.ErrorContains(t, err, "finish the running update")
}

func TestManagedUpgradeHandsOffOnlyObservation(t *testing.T) {
	withCLIVersion(t, "v0.4.0")
	f := newDispatchFixture(t, "v0.4.0", "cluster", "upgrade", "--version", "v0.5.0", "--wait", "--yes")
	f.d.executable = writeExecutable(t, t.TempDir(), "skali", fakeCLI("v0.5.0"))
	f.seedCache(t, "v0.5.0")
	submissions := 0
	status := updates.Status{Managed: true, Manageable: true, Installed: updates.Installed{Version: "v0.4.0", PlatformVersion: "v0.4.0"}, Summary: updates.Summary{State: "available", Action: "update", TargetVersion: "v0.5.0", ConvergedVersion: "v0.4.0"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		release := "v0.4.0"
		if submissions > 0 {
			release = "v0.5.0"
		}
		w.Header().Set(client.VersionHeader, release)
		w.Header().Set(client.InstanceHeader, "inst-1")
		if r.URL.Path == "/healthz" {
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		if submissions > 0 && r.Header.Get(client.ClientVersionHeader) != "v0.5.0" {
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"error":{"code":"cli_version_mismatch","message":"requires skali v0.5.0"}}`))
			return
		}
		if r.Method == "POST" {
			require.Equal(t, "/v1/system/updates/apply", r.URL.Path)
			submissions++
			status.Operation = &updates.OperationState{ID: "accepted-once", Phase: "complete", TargetVersion: "v0.5.0"}
			status.Summary.State = "current"
			status.Summary.Action = ""
		}
		_ = json.NewEncoder(w).Encode(status)
	}))
	defer server.Close()
	f.seedRemote(t, "target", server.URL, "v0.4.0")
	previous := invocationContext
	invocationContext = &versionContext{Home: f.d.executable, Remote: "target", Master: server.URL, Instance: "inst-1", Release: "v0.4.0", Source: "--remote", Mode: "verified"}
	t.Cleanup(func() { invocationContext = previous })
	factory := newObservationDispatcher
	t.Cleanup(func() { newObservationDispatcher = factory })
	newObservationDispatcher = func(args []string) (*dispatcher, error) { f.d.args = args; return f.d, nil }
	f.script = []func(spawnCall) (childStatus, error){func(call spawnCall) (childStatus, error) {
		require.Equal(t, []string{"cluster", "upgrade", "--observe-operation", "accepted-once", "--remote", "target"}, call.args)
		api := client.New(server.URL, "tok", client.Caller{Version: "v0.5.0"})
		return childStatus{}, waitAPIUpdate(context.Background(), &bytes.Buffer{}, api, "accepted-once")
	}}
	cfg, err := cliconfig.Load()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err = runManagedUpdate(ctx, &bytes.Buffer{}, bufio.NewReader(strings.NewReader("")), remoteClient(cfg, cfg.Remotes["target"]), "v0.5.0", true, true)
	var dispatched *dispatchedExit
	require.ErrorAs(t, err, &dispatched)
	require.Zero(t, dispatched.code)
	require.Equal(t, 1, submissions)
	require.Len(t, f.spawns, 1)
}
