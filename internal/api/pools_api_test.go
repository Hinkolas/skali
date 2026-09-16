package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/pgtune"
	"github.com/Hinkolas/skali/internal/substrate"
)

// poolsTestAPI wires a real (idle) substrate controller as the PoolTuner
// on a managed cluster: it sizes pools from the observed nodes and its
// queue records the enqueue without any worker running.
func poolsTestAPI(t *testing.T) (*testAPI, *substrate.Controller) {
	t.Helper()
	var ctl *substrate.Controller
	a := newTestAPIWith(t, "test", func(deps *Deps) {
		ctl = substrate.New(substrate.Deps{
			DB:       deps.Databases,
			Observed: deps.RuntimeLogs.Observed,
		}, substrate.Config{Managed: true})
		deps.Pools = ctl
		deps.ManagedCluster = true
	})
	return a, ctl
}

func seedPool(t *testing.T, a *testAPI, name, class string) {
	t.Helper()
	_, err := dbstore.New(a.st).CreateCluster(context.Background(), dbstore.ClusterInput{
		Name: name, Engine: "postgres", Major: 17, Class: class,
		Instances: 2, StorageBytes: 100 << 30, Image: "ghcr.io/cloudnative-pg/postgresql:17.9-system-trixie",
	})
	require.NoError(t, err)
}

func seedDatabaseNode(a *testAPI, name string, allocatable int64) {
	a.observed.Store.SetNodeCapabilities(name, []string{layout.CapabilityDatabase})
	a.observed.Store.SetNodeRecord(observe.NodeRecord{Name: name, Ready: true, Schedulable: true,
		Capabilities: []string{layout.CapabilityDatabase}, MemoryAllocatableBytes: allocatable})
}

func TestDatabasePoolsRequireAdmin(t *testing.T) {
	a := newTestAPI(t)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	a.createUser("member@example.com", "hunter2hunter2")
	member := a.login("member@example.com", "hunter2hunter2")

	status, body := a.do("GET", "/v1/system/database-pools", member, nil)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "forbidden", errorCode(t, body))
	status, body = a.do("PUT", "/v1/system/database-pools/pg17-shared/settings", member, map[string]any{"parameters": map[string]string{}})
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "forbidden", errorCode(t, body))
}

func TestDatabasePoolSettingsRequireFreshAuth(t *testing.T) {
	a := newTestAPI(t)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	admin := a.login("admin@example.com", "hunter2hunter2")
	a.staleAllSessions()

	status, body := a.do("PUT", "/v1/system/database-pools/pg17-shared/settings", admin, map[string]any{"memory_bytes": nil})
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "reauth_required", errorCode(t, body))
}

func TestDatabasePoolsListSizesFromNodes(t *testing.T) {
	a, _ := poolsTestAPI(t)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	admin := a.login("admin@example.com", "hunter2hunter2")

	status, body := a.do("GET", "/v1/system/database-pools", admin, nil)
	require.Equal(t, http.StatusOK, status)
	require.Empty(t, body["pools"])

	seedPool(t, a, "pg17-shared", pgtune.ClassShared)

	// No database node observed yet: the budget is unknown, nothing is
	// derived, the overrides and allowlist still show.
	status, body = a.do("GET", "/v1/system/database-pools", admin, nil)
	require.Equal(t, http.StatusOK, status)
	pools := body["pools"].([]any)
	require.Len(t, pools, 1)
	pool := pools[0].(map[string]any)
	require.Equal(t, "pg17-shared", pool["name"])
	require.Equal(t, "shared", pool["class"])
	memory := pool["memory"].(map[string]any)
	require.Nil(t, memory["bytes"])
	require.Equal(t, true, memory["auto"])
	parameters := pool["parameters"].(map[string]any)
	require.Empty(t, parameters["effective"])
	require.Empty(t, parameters["overrides"])
	require.Contains(t, parameters["allowed"], "shared_buffers")
	require.Equal(t, []any{"max_connections", "max_worker_processes", "shared_buffers", "wal_buffers"}, parameters["restart_keys"])
	require.Nil(t, pool["observed"])

	// An 8 GB node: 7782 MiB allocatable, 1 GiB reserve, the shared pool
	// takes half on the 128 MiB grid.
	seedDatabaseNode(a, "db-1", 7782<<20)
	status, body = a.do("GET", "/v1/system/database-pools", admin, nil)
	require.Equal(t, http.StatusOK, status)
	pool = body["pools"].([]any)[0].(map[string]any)
	memory = pool["memory"].(map[string]any)
	require.EqualValues(t, 3328<<20, memory["bytes"])
	require.Equal(t, true, memory["auto"])
	require.Equal(t, "db-1", memory["node"])
	effective := pool["parameters"].(map[string]any)["effective"].(map[string]any)
	require.Equal(t, "832MB", effective["shared_buffers"])
	require.Equal(t, "2496MB", effective["effective_cache_size"])
	require.Equal(t, "100", effective["max_connections"])
	require.NotContains(t, effective, "wal_keep_size", "the WAL retention cap is dev only")
}

func TestDatabasePoolSettingsRoundTrip(t *testing.T) {
	a, ctl := poolsTestAPI(t)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	admin := a.login("admin@example.com", "hunter2hunter2")
	seedPool(t, a, "pg17-shared", pgtune.ClassShared)
	seedDatabaseNode(a, "db-1", 7782<<20)
	path := "/v1/system/database-pools/pg17-shared/settings"

	status, body := a.do("PUT", path, admin, map[string]any{})
	require.Equal(t, http.StatusBadRequest, status)
	require.Contains(t, errorMessage(t, body), "nothing to update")

	status, body = a.do("PUT", "/v1/system/database-pools/nope/settings", admin, map[string]any{"memory_bytes": nil})
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "not_found", errorCode(t, body))

	// Validation names the key and the reason, in a 422.
	for name, tc := range map[string]struct {
		body     map[string]any
		contains string
	}{
		"unknown key":  {map[string]any{"parameters": map[string]string{"wal_level": "logical"}}, "wal_level: not a tunable"},
		"k8s unit":     {map[string]any{"parameters": map[string]string{"work_mem": "64Mi"}}, "work_mem: expects an integer with a kB, MB, GB or TB unit"},
		"budget cap":   {map[string]any{"parameters": map[string]string{"shared_buffers": "3000MB"}}, "exceeds 75% of the pool budget"},
		"tiny budget":  {map[string]any{"memory_bytes": 1 << 20}, "below the minimum budget of 256MB"},
		"huge budget":  {map[string]any{"memory_bytes": 64 << 30}, "exceeds the 6758MB available to pools on the smallest database node db-1"},
		"not a number": {map[string]any{"memory_bytes": "4Gi"}, "memory_bytes must be an integer number of bytes or null"},
	} {
		t.Run(name, func(t *testing.T) {
			status, body := a.do("PUT", path, admin, tc.body)
			require.Contains(t, []int{http.StatusBadRequest, http.StatusUnprocessableEntity}, status, "body: %v", body)
			require.Contains(t, errorMessage(t, body), tc.contains)
		})
	}

	// An explicit budget with overrides: the response is the retuned pool
	// and the substrate is poked.
	status, body = a.do("PUT", path, admin, map[string]any{
		"memory_bytes": 4 << 30,
		"parameters":   map[string]string{"work_mem": " 32MB ", "max_connections": "200"},
	})
	require.Equal(t, http.StatusOK, status, "body: %v", body)
	pool := body["pool"].(map[string]any)
	memory := pool["memory"].(map[string]any)
	require.EqualValues(t, 4<<30, memory["bytes"])
	require.Equal(t, false, memory["auto"])
	require.NotContains(t, memory, "node")
	parameters := pool["parameters"].(map[string]any)
	require.Equal(t, map[string]any{"work_mem": "32MB", "max_connections": "200"}, parameters["overrides"], "values are trimmed")
	effective := parameters["effective"].(map[string]any)
	require.Equal(t, "1024MB", effective["shared_buffers"])
	require.Equal(t, "32MB", effective["work_mem"])
	require.Equal(t, "200", effective["max_connections"])
	require.Equal(t, 1, ctl.QueueLen(), "the pool is enqueued for an immediate re-apply")

	// Parameters alone keep the budget; memory null returns to automatic.
	status, body = a.do("PUT", path, admin, map[string]any{"parameters": map[string]string{}})
	require.Equal(t, http.StatusOK, status)
	pool = body["pool"].(map[string]any)
	require.EqualValues(t, 4<<30, pool["memory"].(map[string]any)["bytes"])
	require.Empty(t, pool["parameters"].(map[string]any)["overrides"])

	status, body = a.do("PUT", path, admin, map[string]any{"memory_bytes": nil})
	require.Equal(t, http.StatusOK, status)
	memory = body["pool"].(map[string]any)["memory"].(map[string]any)
	require.Equal(t, true, memory["auto"])
	require.EqualValues(t, 3328<<20, memory["bytes"])
	require.Equal(t, "db-1", memory["node"])

	// The list agrees with the last write.
	status, body = a.do("GET", "/v1/system/database-pools", admin, nil)
	require.Equal(t, http.StatusOK, status)
	pool = body["pools"].([]any)[0].(map[string]any)
	require.Equal(t, true, pool["memory"].(map[string]any)["auto"])
}
