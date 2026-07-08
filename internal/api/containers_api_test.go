package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/store"
)

// seedContainer plants one observed container row on the master's own node,
// standing in for the reconciler's write-through / a heartbeat report.
func seedContainer(t *testing.T, a *testAPI, name string) {
	t.Helper()
	labels, err := json.Marshal(map[string]string{"skali.managed": "true", "skali.kind": "application"})
	require.NoError(t, err)
	require.NoError(t, a.st.UpsertNodeContainer(t.Context(), store.UpsertNodeContainerParams{
		NodeID: a.selfID, ContainerID: "ctr-" + name, Name: name,
		Image: "nginx:alpine", Kind: "application", State: "running", Labels: labels,
	}))
}

func TestContainersReadsRequireAdmin(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("member@example.com", "hunter2hunter2")
	token := a.login("member@example.com", "hunter2hunter2")

	for _, path := range []string{"/v1/containers", "/v1/images", "/v1/volumes"} {
		status, body := a.do("GET", path, token, nil)
		require.Equal(t, http.StatusForbidden, status, path)
		require.Equal(t, "forbidden", errorCode(t, body), path)
	}
}

func TestContainersListAndNodeFilter(t *testing.T) {
	a := newTestAPI(t)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	token := a.login("admin@example.com", "hunter2hunter2")
	seedContainer(t, a, "web")

	status, body := a.do("GET", "/v1/containers", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Len(t, body["containers"].([]any), 1)
	row := body["containers"].([]any)[0].(map[string]any)
	require.Equal(t, "web", row["name"])
	require.Equal(t, a.selfID.String(), row["node_id"])
	require.NotEmpty(t, row["node_name"])
	require.Equal(t, "true", row["labels"].(map[string]any)["skali.managed"])

	status, body = a.do("GET", "/v1/containers?node="+a.selfID.String(), token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Len(t, body["containers"].([]any), 1)

	// Unknown node → 404 (an empty list must mean "nothing there", never a
	// typoed id); malformed → 400.
	status, body = a.do("GET", "/v1/containers?node=00000000-0000-0000-0000-000000000000", token, nil)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "not_found", errorCode(t, body))
	status, body = a.do("GET", "/v1/containers?node=not-a-uuid", token, nil)
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "bad_request", errorCode(t, body))
}
