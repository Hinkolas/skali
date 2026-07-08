package api

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestWorkloadsRequireAdmin(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("member@example.com", "hunter2hunter2")
	token := a.login("member@example.com", "hunter2hunter2")

	for _, tc := range []struct{ method, path string }{
		{"GET", "/v1/workloads"},
		{"GET", "/v1/workloads/" + uuid.NewString()},
		{"POST", "/v1/workloads"},
		{"PATCH", "/v1/workloads/" + uuid.NewString()},
		{"DELETE", "/v1/workloads/" + uuid.NewString()},
	} {
		status, body := a.do(tc.method, tc.path, token, map[string]any{})
		require.Equal(t, http.StatusForbidden, status, "%s %s", tc.method, tc.path)
		require.Equal(t, "forbidden", errorCode(t, body), "%s %s", tc.method, tc.path)
	}
}

func TestWorkloadWritesGatedBySudoMode(t *testing.T) {
	a := newTestAPI(t)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	token := a.login("admin@example.com", "hunter2hunter2")
	a.staleAllSessions()

	status, _ := a.do("GET", "/v1/workloads", token, nil)
	require.Equal(t, http.StatusOK, status, "reads stay open for a stale admin")

	status, body := a.do("POST", "/v1/workloads", token, map[string]any{
		"name": "blog", "kind": "application", "image": "nginx:1",
	})
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "reauth_required", errorCode(t, body))
}

func TestWorkloadLifecycleViaAPI(t *testing.T) {
	a := newTestAPI(t)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	token := a.login("admin@example.com", "hunter2hunter2")

	status, body := a.do("POST", "/v1/workloads", token, map[string]any{
		"name": "blog", "kind": "application", "image": "nginx:1",
		"replicas": 2, "constraints": map[string]any{"node_roles": []string{"worker"}},
		"spec": map[string]any{"env": map[string]string{"PORT": "80"}},
	})
	require.Equal(t, http.StatusCreated, status, "body: %v", body)
	wl := body["workload"].(map[string]any)
	require.Equal(t, "blog", wl["name"])
	require.EqualValues(t, 2, wl["replicas"])
	require.EqualValues(t, 1, wl["generation"])
	require.Equal(t, "importing", wl["status"], "no digest pin yet, no reconciler running")
	require.Equal(t, "running", wl["desired_state"])
	id := wl["id"].(string)

	// Default replicas is 1 when omitted; duplicate names conflict.
	status, body = a.do("POST", "/v1/workloads", token, map[string]any{
		"name": "solo", "kind": "database", "image": "postgres:17",
	})
	require.Equal(t, http.StatusCreated, status)
	require.EqualValues(t, 1, body["workload"].(map[string]any)["replicas"])
	status, body = a.do("POST", "/v1/workloads", token, map[string]any{
		"name": "blog", "kind": "application", "image": "nginx:1",
	})
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "conflict", errorCode(t, body))

	status, body = a.do("GET", "/v1/workloads", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Len(t, body["workloads"].([]any), 2)

	status, body = a.do("GET", "/v1/workloads/"+id, token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "blog", body["workload"].(map[string]any)["name"])

	// Update bumps the generation; spec replaces as a unit.
	status, body = a.do("PATCH", "/v1/workloads/"+id, token, map[string]any{
		"replicas": 3, "spec": map[string]any{"env": map[string]string{"PORT": "8080"}},
	})
	require.Equal(t, http.StatusOK, status, "body: %v", body)
	wl = body["workload"].(map[string]any)
	require.EqualValues(t, 3, wl["replicas"])
	require.EqualValues(t, 2, wl["generation"])
	require.Equal(t, "8080", wl["spec"].(map[string]any)["env"].(map[string]any)["PORT"])

	// Delete is asynchronous: 202, the row survives marked deleting.
	status, _ = a.do("DELETE", "/v1/workloads/"+id, token, nil)
	require.Equal(t, http.StatusAccepted, status)
	status, body = a.do("GET", "/v1/workloads/"+id, token, nil)
	require.Equal(t, http.StatusOK, status)
	wl = body["workload"].(map[string]any)
	require.Equal(t, "deleting", wl["desired_state"])
	require.Equal(t, "deleting", wl["status"])

	// Updating a deleting workload conflicts.
	status, body = a.do("PATCH", "/v1/workloads/"+id, token, map[string]any{"replicas": 1})
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "conflict", errorCode(t, body))
}

func TestWorkloadValidationAndNotFound(t *testing.T) {
	a := newTestAPI(t)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	token := a.login("admin@example.com", "hunter2hunter2")

	for name, req := range map[string]map[string]any{
		"bad name":       {"name": "Bad_Name", "kind": "application", "image": "nginx:1"},
		"system kind":    {"name": "blog", "kind": "system", "image": "nginx:1"},
		"digest ref":     {"name": "blog", "kind": "application", "image": "nginx@sha256:0123456789012345678901234567890123456789012345678901234567890123"},
		"bad desired":    {"name": "blog", "kind": "application", "image": "nginx:1", "desired_state": "paused"},
		"bad replicas":   {"name": "blog", "kind": "application", "image": "nginx:1", "replicas": -1},
		"bad role":       {"name": "blog", "kind": "application", "image": "nginx:1", "constraints": map[string]any{"node_roles": []string{"gpu"}}},
		"reserved label": {"name": "blog", "kind": "application", "image": "nginx:1", "spec": map[string]any{"labels": map[string]string{"skali.kind": "x"}}},
	} {
		status, body := a.do("POST", "/v1/workloads", token, req)
		require.Equal(t, http.StatusBadRequest, status, "%s: %v", name, body)
		require.Equal(t, "bad_request", errorCode(t, body), name)
	}

	status, body := a.do("GET", "/v1/workloads/"+uuid.NewString(), token, nil)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "not_found", errorCode(t, body))
	status, _ = a.do("GET", "/v1/workloads/not-a-uuid", token, nil)
	require.Equal(t, http.StatusNotFound, status)
	status, _ = a.do("PATCH", "/v1/workloads/"+uuid.NewString(), token, map[string]any{"replicas": 1})
	require.Equal(t, http.StatusNotFound, status)
	status, _ = a.do("DELETE", "/v1/workloads/"+uuid.NewString(), token, nil)
	require.Equal(t, http.StatusNotFound, status)
}

func TestWorkloadsDisabledWithoutRegistry(t *testing.T) {
	a := newTestAPIWithRegistry(t, nil)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	token := a.login("admin@example.com", "hunter2hunter2")

	// Reads stay store-only and keep serving.
	status, body := a.do("GET", "/v1/workloads", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Empty(t, body["workloads"])

	// Writes need the mirror.
	status, body = a.do("POST", "/v1/workloads", token, map[string]any{
		"name": "blog", "kind": "application", "image": "nginx:1",
	})
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, "registry_disabled", errorCode(t, body))
	status, body = a.do("DELETE", "/v1/workloads/"+uuid.NewString(), token, nil)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, "registry_disabled", errorCode(t, body))
}
