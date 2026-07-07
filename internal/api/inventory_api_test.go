package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/store"
)

// seedInventory plants observed image/volume rows for the master's own node,
// standing in for the poller's heartbeat recording.
func seedInventory(t *testing.T, a *testAPI) {
	t.Helper()
	ctx := t.Context()
	created := time.Now().Add(-time.Hour)
	require.NoError(t, a.st.UpsertNodeImage(ctx, store.UpsertNodeImageParams{
		NodeID:       a.selfID,
		ImageID:      "sha256:aaa",
		RepoTags:     []string{"nginx:alpine"},
		RepoDigests:  []string{"nginx@sha256:feed"},
		SizeBytes:    100,
		Containers:   1,
		ImageCreated: &created,
	}))
	require.NoError(t, a.st.UpsertNodeImage(ctx, store.UpsertNodeImageParams{
		NodeID:      a.selfID,
		ImageID:     "sha256:bbb",
		RepoTags:    []string{},
		RepoDigests: []string{},
		SizeBytes:   50,
		Dangling:    true,
	}))
	require.NoError(t, a.st.UpsertNodeVolume(ctx, store.UpsertNodeVolumeParams{
		NodeID:     a.selfID,
		Name:       "data",
		Driver:     "local",
		Scope:      "local",
		Mountpoint: "/var/lib/docker/volumes/data/_data",
		Labels:     []byte(`{"app":"demo"}`),
		Containers: 2,
	}))
}

func TestNodeInventoryEndpoints(t *testing.T) {
	a := newTestAPI(t)
	a.createAdmin("admin@example.com", "password123")
	token := a.login("admin@example.com", "password123")
	seedInventory(t, a)

	// Images: largest first, dangling derived rows intact.
	status, body := a.do("GET", "/v1/nodes/"+a.selfID.String()+"/images", token, nil)
	require.Equal(t, http.StatusOK, status, "body: %v", body)
	images := body["images"].([]any)
	require.Len(t, images, 2)
	first := images[0].(map[string]any)
	require.Equal(t, "sha256:aaa", first["id"])
	require.Equal(t, false, first["dangling"])
	require.EqualValues(t, 100, first["size_bytes"])
	require.EqualValues(t, 1, first["containers"])
	require.NotNil(t, first["created_at"])
	second := images[1].(map[string]any)
	require.Equal(t, true, second["dangling"])
	require.Equal(t, []any{}, second["repo_tags"])
	require.Nil(t, second["created_at"])

	// Volumes.
	status, body = a.do("GET", "/v1/nodes/"+a.selfID.String()+"/volumes", token, nil)
	require.Equal(t, http.StatusOK, status, "body: %v", body)
	volumes := body["volumes"].([]any)
	require.Len(t, volumes, 1)
	vol := volumes[0].(map[string]any)
	require.Equal(t, "data", vol["name"])
	require.Equal(t, "local", vol["driver"])
	require.EqualValues(t, 2, vol["containers"])
	require.Equal(t, map[string]any{"app": "demo"}, vol["labels"])

	// Unknown node: 404, not an empty list.
	status, _ = a.do("GET", "/v1/nodes/00000000-0000-0000-0000-000000000000/images", token, nil)
	require.Equal(t, http.StatusNotFound, status)
	status, _ = a.do("GET", "/v1/nodes/00000000-0000-0000-0000-000000000000/volumes", token, nil)
	require.Equal(t, http.StatusNotFound, status)
}

func TestNodeInventoryRequiresAdmin(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("member@example.com", "password123")
	token := a.login("member@example.com", "password123")

	for _, path := range []string{"/images", "/volumes"} {
		status, body := a.do("GET", "/v1/nodes/"+a.selfID.String()+path, token, nil)
		require.Equal(t, http.StatusForbidden, status)
		require.Equal(t, "forbidden", errorCode(t, body))
	}
}
