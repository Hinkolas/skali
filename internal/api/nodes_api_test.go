package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/cluster"
)

// seedNodes creates the master's own row plus one worker, mirroring what
// EnsureSelfNode and an enrollment produce.
func (a *testAPI) seedNodes() (masterID, workerID string) {
	a.t.Helper()
	ctx := a.t.Context()
	master, err := cluster.EnsureSelfNode(ctx, a.st, testClusterAddr)
	require.NoError(a.t, err)

	var worker struct{ ID string }
	err = a.st.Pool.QueryRow(ctx,
		`INSERT INTO nodes (id, name, roles, advertise_addr)
		 VALUES (gen_random_uuid(), 'worker-1', '{worker}', '10.0.0.2:7443')
		 RETURNING id::text`).Scan(&worker.ID)
	require.NoError(a.t, err)
	return master.ID.String(), worker.ID
}

func TestNodesRequireAdmin(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("member@example.com", "hunter2hunter2")
	token := a.login("member@example.com", "hunter2hunter2")

	for _, tc := range []struct{ method, path string }{
		{"GET", "/v1/nodes"},
		{"POST", "/v1/nodes/tokens"},
		{"PATCH", "/v1/nodes/00000000-0000-0000-0000-000000000000"},
		{"DELETE", "/v1/nodes/00000000-0000-0000-0000-000000000000"},
	} {
		status, body := a.do(tc.method, tc.path, token, map[string]any{})
		require.Equal(t, http.StatusForbidden, status, "%s %s", tc.method, tc.path)
		require.Equal(t, "forbidden", errorCode(t, body), "%s %s", tc.method, tc.path)
	}
}

func TestNodesList(t *testing.T) {
	a := newTestAPI(t)
	masterID, workerID := a.seedNodes()
	a.createAdmin("admin@example.com", "hunter2hunter2")
	token := a.login("admin@example.com", "hunter2hunter2")

	status, body := a.do("GET", "/v1/nodes", token, nil)
	require.Equal(t, http.StatusOK, status)
	nodes := body["nodes"].([]any)
	require.Len(t, nodes, 2)

	first := nodes[0].(map[string]any)
	require.Equal(t, masterID, first["id"])
	require.ElementsMatch(t, []any{"master", "worker"}, first["roles"])
	require.Equal(t, "offline", first["status"])
	second := nodes[1].(map[string]any)
	require.Equal(t, workerID, second["id"])
	require.Equal(t, "worker-1", second["name"])
}

func TestNodesWritesGatedBySudoMode(t *testing.T) {
	a := newTestAPI(t)
	_, workerID := a.seedNodes()
	a.createAdmin("admin@example.com", "hunter2hunter2")
	token := a.login("admin@example.com", "hunter2hunter2")
	a.staleAllSessions()

	// Reads stay open to a stale admin session…
	status, _ := a.do("GET", "/v1/nodes", token, nil)
	require.Equal(t, http.StatusOK, status)

	// …but every write demands recent authentication.
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/v1/nodes/tokens", map[string]any{}},
		{"PATCH", "/v1/nodes/" + workerID, map[string]any{"name": "x"}},
		{"DELETE", "/v1/nodes/" + workerID, nil},
	} {
		status, body := a.do(tc.method, tc.path, token, tc.body)
		require.Equal(t, http.StatusForbidden, status, "%s %s", tc.method, tc.path)
		require.Equal(t, "reauth_required", errorCode(t, body), "%s %s", tc.method, tc.path)
	}
}

func TestCreateJoinToken(t *testing.T) {
	a := newTestAPI(t)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	token := a.login("admin@example.com", "hunter2hunter2")

	status, body := a.do("POST", "/v1/nodes/tokens", token, map[string]any{"roles": []string{"worker", "edge"}})
	require.Equal(t, http.StatusCreated, status, "body: %v", body)

	joinToken := body["token"].(string)
	require.Len(t, strings.Split(joinToken, "."), 3, "token must be <id>.<secret>.<ca-fp>")
	require.NotEmpty(t, body["expires_at"])
	require.Equal(t,
		fmt.Sprintf("skalid enroll --master %s --token %s", testClusterAddr, joinToken),
		body["enroll_command"])

	// Invalid roles are rejected; master is never grantable.
	status, body = a.do("POST", "/v1/nodes/tokens", token, map[string]any{"roles": []string{"master"}})
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "bad_request", errorCode(t, body))
}

func TestUpdateNode(t *testing.T) {
	a := newTestAPI(t)
	masterID, workerID := a.seedNodes()
	a.createAdmin("admin@example.com", "hunter2hunter2")
	token := a.login("admin@example.com", "hunter2hunter2")

	// Rename + grant edge + set public_addr on the worker.
	status, body := a.do("PATCH", "/v1/nodes/"+workerID, token, map[string]any{
		"name":        "edge-1",
		"roles":       []string{"worker", "edge"},
		"public_addr": "203.0.113.10:443",
	})
	require.Equal(t, http.StatusOK, status, "body: %v", body)
	node := body["node"].(map[string]any)
	require.Equal(t, "edge-1", node["name"])
	require.ElementsMatch(t, []any{"worker", "edge"}, node["roles"])
	require.Equal(t, "203.0.113.10:443", node["public_addr"])

	// Clearing public_addr with an empty string.
	status, body = a.do("PATCH", "/v1/nodes/"+workerID, token, map[string]any{"public_addr": ""})
	require.Equal(t, http.StatusOK, status)
	require.Nil(t, body["node"].(map[string]any)["public_addr"])

	// The master role is immutable, both directions.
	status, body = a.do("PATCH", "/v1/nodes/"+workerID, token, map[string]any{"roles": []string{"master", "worker"}})
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "conflict", errorCode(t, body))
	status, body = a.do("PATCH", "/v1/nodes/"+masterID, token, map[string]any{"roles": []string{"worker"}})
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "conflict", errorCode(t, body))

	// Empty update and unknown node.
	status, body = a.do("PATCH", "/v1/nodes/"+workerID, token, map[string]any{})
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "bad_request", errorCode(t, body))
	status, body = a.do("PATCH", "/v1/nodes/00000000-0000-0000-0000-000000000001", token, map[string]any{"name": "x"})
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "not_found", errorCode(t, body))
}

func TestDeleteNode(t *testing.T) {
	a := newTestAPI(t)
	masterID, workerID := a.seedNodes()
	a.createAdmin("admin@example.com", "hunter2hunter2")
	token := a.login("admin@example.com", "hunter2hunter2")

	// The master node is protected.
	status, body := a.do("DELETE", "/v1/nodes/"+masterID, token, nil)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "conflict", errorCode(t, body))

	// Workers delete fine; a second delete 404s; malformed ids 404 too.
	status, _ = a.do("DELETE", "/v1/nodes/"+workerID, token, nil)
	require.Equal(t, http.StatusNoContent, status)
	status, _ = a.do("DELETE", "/v1/nodes/"+workerID, token, nil)
	require.Equal(t, http.StatusNotFound, status)
	status, _ = a.do("DELETE", "/v1/nodes/not-a-uuid", token, nil)
	require.Equal(t, http.StatusNotFound, status)
}
