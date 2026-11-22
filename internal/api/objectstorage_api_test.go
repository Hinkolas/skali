package api

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
)

// fakeObjectStorage serves a fixed slot report and records settings
// wake-ups.
type fakeObjectStorage struct {
	mu       sync.Mutex
	slots    seaweed.SlotReport
	err      error
	enqueued int
}

func (f *fakeObjectStorage) ObjectStoreSlots(context.Context) (seaweed.SlotReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.slots, f.err
}

func (f *fakeObjectStorage) EnqueueObjectStoreSettings() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enqueued++
}

func objectStorageTestAPI(t *testing.T) (*testAPI, *fakeObjectStorage) {
	t.Helper()
	storage := &fakeObjectStorage{}
	a := newTestAPIWith(t, "test", func(deps *Deps) {
		deps.ObjectStorage = storage
		deps.ManagedCluster = true
	})
	return a, storage
}

func seedObjectStore(t *testing.T, a *testAPI) {
	t.Helper()
	_, err := dbstore.New(a.st).CreateObjectStore(context.Background(), dbstore.StoreInput{
		Name: seaweed.StoreName, Masters: 3, VolumeServers: 2, Replication: "001", Image: seaweed.Image,
	})
	require.NoError(t, err)
}

func TestObjectStorageRequiresAdmin(t *testing.T) {
	a, _ := objectStorageTestAPI(t)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	a.createUser("member@example.com", "hunter2hunter2")
	member := a.login("member@example.com", "hunter2hunter2")

	status, body := a.do("GET", "/v1/system/object-storage", member, nil)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "forbidden", errorCode(t, body))
	status, body = a.do("PUT", "/v1/system/object-storage/settings", member, map[string]any{"volume_size_limit_mb": 512})
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "forbidden", errorCode(t, body))
}

func TestObjectStorageSettingsRequireFreshAuth(t *testing.T) {
	a, _ := objectStorageTestAPI(t)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	admin := a.login("admin@example.com", "hunter2hunter2")
	a.staleAllSessions()

	status, body := a.do("PUT", "/v1/system/object-storage/settings", admin, map[string]any{"volume_size_limit_mb": 512})
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "reauth_required", errorCode(t, body))
}

func TestObjectStorageCapacityAndVolumeSize(t *testing.T) {
	a, storage := objectStorageTestAPI(t)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	admin := a.login("admin@example.com", "hunter2hunter2")

	// No store on the installation yet.
	status, body := a.do("GET", "/v1/system/object-storage", admin, nil)
	require.Equal(t, http.StatusNotFound, status, "%v", body)

	seedObjectStore(t, a)
	storage.slots = seaweed.SlotReport{Servers: []seaweed.ServerSlots{
		{URL: "10.0.0.1:8080", Max: 20, Volumes: 6},
		{URL: "10.0.0.2:8080", Max: 10, Volumes: 6},
	}}

	status, body = a.do("GET", "/v1/system/object-storage", admin, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	store := body["store"].(map[string]any)
	require.Equal(t, "001", store["replication"])
	require.Equal(t, true, store["managed"])
	require.EqualValues(t, 0, store["buckets"])
	volumeSize := store["volume_size"].(map[string]any)
	require.EqualValues(t, seaweed.DefaultVolumeSizeLimitMB, volumeSize["limit_mb"])
	require.Equal(t, true, volumeSize["default"])
	require.EqualValues(t, seaweed.MinVolumeSizeLimitMB, volumeSize["min_mb"])
	require.EqualValues(t, seaweed.MaxVolumeSizeLimitMB, volumeSize["max_mb"])
	capacity := store["capacity"].(map[string]any)
	require.EqualValues(t, 30, capacity["slots"])
	require.EqualValues(t, 12, capacity["used_slots"])
	require.EqualValues(t, 18, capacity["free_slots"])
	// Two copies on different servers: the smaller server's four free
	// slots bound the pairs.
	require.EqualValues(t, 4, capacity["volume_headroom"])
	require.EqualValues(t, 4, capacity["bucket_headroom"])
	require.EqualValues(t, 2, capacity["copies"])
	require.EqualValues(t, 1, capacity["volumes_per_bucket"])
	require.Len(t, capacity["servers"], 2)
	require.Nil(t, store["observed"])

	// Out of range and malformed requests change nothing.
	status, body = a.do("PUT", "/v1/system/object-storage/settings", admin, map[string]any{"volume_size_limit_mb": 10})
	require.Equal(t, http.StatusUnprocessableEntity, status, "%v", body)
	status, body = a.do("PUT", "/v1/system/object-storage/settings", admin, map[string]any{"volume_size_limit_mb": "big"})
	require.Equal(t, http.StatusBadRequest, status, "%v", body)
	status, body = a.do("PUT", "/v1/system/object-storage/settings", admin, map[string]any{})
	require.Equal(t, http.StatusBadRequest, status, "%v", body)
	require.Zero(t, storage.enqueued)

	// A set limit is recorded and wakes the substrate.
	status, body = a.do("PUT", "/v1/system/object-storage/settings", admin, map[string]any{"volume_size_limit_mb": 1024})
	require.Equal(t, http.StatusOK, status, "%v", body)
	volumeSize = body["store"].(map[string]any)["volume_size"].(map[string]any)
	require.EqualValues(t, 1024, volumeSize["limit_mb"])
	require.Equal(t, false, volumeSize["default"])
	require.Equal(t, 1, storage.enqueued)
	row, err := dbstore.New(a.st).LiveObjectStore(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 1024, *row.VolumeSizeLimitMb)

	// null returns the store to the default.
	status, body = a.do("PUT", "/v1/system/object-storage/settings", admin, map[string]any{"volume_size_limit_mb": nil})
	require.Equal(t, http.StatusOK, status, "%v", body)
	volumeSize = body["store"].(map[string]any)["volume_size"].(map[string]any)
	require.EqualValues(t, seaweed.DefaultVolumeSizeLimitMB, volumeSize["limit_mb"])
	require.Equal(t, true, volumeSize["default"])
	require.Equal(t, 2, storage.enqueued)

	// A master that cannot be read leaves the capacity unknown, not zero.
	storage.err = errors.New("master unreachable")
	status, body = a.do("GET", "/v1/system/object-storage", admin, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.Nil(t, body["store"].(map[string]any)["capacity"])
}
