package api

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// createTestContainer posts a minimal valid create request to the master's
// own node (the local-handle path over the fake engine).
func (a *testAPI) createTestContainer(token, name string) map[string]any {
	a.t.Helper()
	status, body := a.do("POST", fmt.Sprintf("/v1/nodes/%s/containers", a.selfID), token, map[string]any{
		"name":  name,
		"image": "nginx:alpine",
		"kind":  "application",
	})
	require.Equal(a.t, http.StatusCreated, status, "body: %v", body)
	return body["container"].(map[string]any)
}

func TestContainersRequireAdmin(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("member@example.com", "hunter2hunter2")
	token := a.login("member@example.com", "hunter2hunter2")

	base := fmt.Sprintf("/v1/nodes/%s/containers", a.selfID)
	for _, tc := range []struct{ method, path string }{
		{"GET", base},
		{"POST", base},
		{"POST", base + "/abc/start"},
		{"POST", base + "/abc/stop"},
		{"DELETE", base + "/abc"},
	} {
		status, body := a.do(tc.method, tc.path, token, map[string]any{})
		require.Equal(t, http.StatusForbidden, status, "%s %s", tc.method, tc.path)
		require.Equal(t, "forbidden", errorCode(t, body), "%s %s", tc.method, tc.path)
	}
}

func TestContainersWritesGatedBySudoMode(t *testing.T) {
	a := newTestAPI(t)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	token := a.login("admin@example.com", "hunter2hunter2")

	a.staleAllSessions()

	// Reads stay open for a stale admin.
	status, _ := a.do("GET", fmt.Sprintf("/v1/nodes/%s/containers", a.selfID), token, nil)
	require.Equal(t, http.StatusOK, status)

	status, body := a.do("POST", fmt.Sprintf("/v1/nodes/%s/containers", a.selfID), token, map[string]any{
		"name": "web", "image": "nginx:alpine", "kind": "application",
	})
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "reauth_required", errorCode(t, body))
}

func TestContainerLifecycleViaAPI(t *testing.T) {
	a := newTestAPI(t)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	token := a.login("admin@example.com", "hunter2hunter2")

	ctr := a.createTestContainer(token, "web")
	require.Equal(t, "web", ctr["name"])
	require.Equal(t, "running", ctr["state"])
	require.Equal(t, "application", ctr["kind"])
	labels := ctr["labels"].(map[string]any)
	require.Equal(t, "true", labels["skali.managed"])
	require.Equal(t, "application", labels["skali.kind"])
	cid := ctr["id"].(string)
	base := fmt.Sprintf("/v1/nodes/%s/containers", a.selfID)

	// Observed state is written through immediately.
	status, body := a.do("GET", base, token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Len(t, body["containers"].([]any), 1)

	status, body = a.do("POST", fmt.Sprintf("%s/%s/stop", base, cid), token, nil)
	require.Equal(t, http.StatusOK, status, "body: %v", body)
	require.Equal(t, "exited", body["container"].(map[string]any)["state"])

	status, body = a.do("POST", fmt.Sprintf("%s/%s/start", base, cid), token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "running", body["container"].(map[string]any)["state"])

	// Removing a running container needs force.
	status, body = a.do("DELETE", fmt.Sprintf("%s/%s", base, cid), token, nil)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "conflict", errorCode(t, body))

	status, _ = a.do("DELETE", fmt.Sprintf("%s/%s?force=true", base, cid), token, nil)
	require.Equal(t, http.StatusNoContent, status)
	_, ok := a.eng.Get(cid)
	require.False(t, ok, "container should be gone from the engine")
	status, body = a.do("GET", base, token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Empty(t, body["containers"], "observed row deleted with the container")
}

func TestContainerCreateValidation(t *testing.T) {
	a := newTestAPI(t)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	token := a.login("admin@example.com", "hunter2hunter2")
	base := fmt.Sprintf("/v1/nodes/%s/containers", a.selfID)

	valid := func() map[string]any {
		return map[string]any{"name": "ok", "image": "nginx:alpine", "kind": "application"}
	}
	for name, mutate := range map[string]func(m map[string]any){
		"missing kind":      func(m map[string]any) { delete(m, "kind") },
		"bogus kind":        func(m map[string]any) { m["kind"] = "bogus" },
		"reserved label":    func(m map[string]any) { m["labels"] = map[string]string{"skali.kind": "system"} },
		"bad name":          func(m map[string]any) { m["name"] = "-nope" },
		"empty image":       func(m map[string]any) { m["image"] = "" },
		"bogus pull policy": func(m map[string]any) { m["pull"] = "sometimes" },
	} {
		req := valid()
		mutate(req)
		status, body := a.do("POST", base, token, req)
		require.Equal(t, http.StatusBadRequest, status, "%s: body %v", name, body)
		require.Equal(t, "bad_request", errorCode(t, body), name)
	}

	// Unknown node and unknown container are 404s.
	status, body := a.do("POST", "/v1/nodes/00000000-0000-0000-0000-000000000000/containers", token, valid())
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "not_found", errorCode(t, body), "unknown node")

	status, body = a.do("POST", base+"/nope/start", token, nil)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "not_found", errorCode(t, body), "unknown container")

	// Remove is the idempotent escape hatch: unknown container still 204s.
	status, _ = a.do("DELETE", base+"/nope", token, nil)
	require.Equal(t, http.StatusNoContent, status)
}

func TestContainerOpsOnUnreachableNode(t *testing.T) {
	a := newTestAPI(t)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	token := a.login("admin@example.com", "hunter2hunter2")

	// A worker that fails fast: connection refused on a closed local port,
	// with a serial so the pool dials rather than refusing outright.
	var workerID string
	err := a.st.Pool.QueryRow(a.t.Context(),
		`INSERT INTO nodes (id, name, roles, advertise_addr, cert_serial)
		 VALUES (gen_random_uuid(), 'worker-dead', '{worker}', '127.0.0.1:1', 'deadbeef')
		 RETURNING id::text`).Scan(&workerID)
	require.NoError(t, err)

	status, body := a.do("POST", fmt.Sprintf("/v1/nodes/%s/containers", workerID), token, map[string]any{
		"name": "web", "image": "nginx:alpine", "kind": "application",
	})
	require.Equal(t, http.StatusBadGateway, status)
	require.Equal(t, "node_unreachable", errorCode(t, body))
}
