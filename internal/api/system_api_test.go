package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/observe"
)

func TestListNodes(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("nick@example.com", "hunter2hunter2")
	token := a.login("nick@example.com", "hunter2hunter2")

	// Empty cluster: an empty list plus the observation block.
	status, body := a.do("GET", "/v1/nodes", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Empty(t, body["nodes"])
	observation := body["observation"].(map[string]any)
	require.Equal(t, "unknown", observation["state"])

	heartbeat := time.Unix(1700000000, 0).UTC()
	a.observed.SetNodeRecord(observe.NodeRecord{
		Name: "cp-1", Role: "server", Capabilities: []string{"edge"},
		Arch: "arm64", OS: "Ubuntu 24.04 LTS", KubeletVersion: "v1.31.4+k3s1",
		Ready: true, Schedulable: true,
		InternalIP: "10.0.0.1", ExternalIP: "203.0.113.1",
		LastHeartbeat: heartbeat,
	})
	a.observed.SetNodeRecord(observe.NodeRecord{
		Name: "app-1", Role: "agent", Ready: false, Schedulable: true,
	})

	status, body = a.do("GET", "/v1/nodes", token, nil)
	require.Equal(t, http.StatusOK, status)
	nodes := body["nodes"].([]any)
	require.Len(t, nodes, 2)

	first := nodes[0].(map[string]any)
	require.Equal(t, "app-1", first["name"], "nodes sort by name")
	require.Equal(t, "agent", first["role"])
	require.Equal(t, false, first["ready"])
	require.Nil(t, first["last_heartbeat"])
	require.NotContains(t, first, "arch")

	second := nodes[1].(map[string]any)
	require.Equal(t, "cp-1", second["name"])
	require.Equal(t, "server", second["role"])
	require.Equal(t, []any{"edge"}, second["capabilities"])
	require.Equal(t, "arm64", second["arch"])
	require.Equal(t, true, second["ready"])
	require.Equal(t, "10.0.0.1", second["internal_ip"])
	require.NotNil(t, second["last_heartbeat"])
}

func TestSystemMeta(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("nick@example.com", "hunter2hunter2")

	status, body := a.do("GET", "/v1/system/meta", "", nil)
	require.Equal(t, http.StatusUnauthorized, status)
	require.Equal(t, "invalid_token", errorCode(t, body))

	token := a.login("nick@example.com", "hunter2hunter2")
	status, body = a.do("GET", "/v1/system/meta", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "test", body["version"])
	require.Equal(t, "Test Instance", body["name"])
}
