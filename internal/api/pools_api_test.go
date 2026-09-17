package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/pgtune"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate"
	"github.com/Hinkolas/skali/internal/substrate/cnpg"
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

// poolsTestAPIWithCluster is poolsTestAPI with a fake Kubernetes cluster
// behind the substrate, so member reads see the given pods.
func poolsTestAPIWithCluster(t *testing.T, objects ...runtime.Object) *testAPI {
	t.Helper()
	return newTestAPIWith(t, "test", func(deps *Deps) {
		deps.Pools = substrate.New(substrate.Deps{
			DB:       deps.Databases,
			Observed: deps.RuntimeLogs.Observed,
			Cluster:  substrate.KubeCluster{Client: &kube.Client{Clientset: kubefake.NewClientset(objects...)}},
		}, substrate.Config{Managed: true})
		deps.ManagedCluster = true
	})
}

func instancePod(name, pool, role, node string, ready bool, restarts int32) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: substrate.Namespace,
			Labels:    map[string]string{cnpg.LabelCluster: pool, cnpg.LabelPodRole: cnpg.PodRoleInstance, cnpg.LabelInstanceRole: role},
		},
		Spec: corev1.PodSpec{NodeName: node},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: map[bool]corev1.ConditionStatus{true: corev1.ConditionTrue, false: corev1.ConditionFalse}[ready]}},
			ContainerStatuses: []corev1.ContainerStatus{{RestartCount: restarts}},
		},
	}
}

func TestDatabasePoolDetailRequiresAdmin(t *testing.T) {
	a := newTestAPI(t)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	a.createUser("member@example.com", "hunter2hunter2")
	member := a.login("member@example.com", "hunter2hunter2")

	for _, path := range []string{"/v1/system/database-pools/pg17-shared", "/v1/system/database-pools/pg17-shared/metrics"} {
		status, body := a.do("GET", path, member, nil)
		require.Equal(t, http.StatusForbidden, status, path)
		require.Equal(t, "forbidden", errorCode(t, body))
	}
}

func TestDatabasePoolGet(t *testing.T) {
	a, _ := poolsTestAPI(t)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	token := a.login("admin@example.com", "hunter2hunter2")

	status, body := a.do("GET", "/v1/system/database-pools/pg17-shared", token, nil)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "not_found", errorCode(t, body))

	seedPool(t, a, "pg17-shared", pgtune.ClassShared)
	status, body = a.do("GET", "/v1/system/database-pools/pg17-shared", token, nil)
	require.Equal(t, http.StatusOK, status)
	pool := body["pool"].(map[string]any)
	require.Equal(t, "pg17-shared", pool["name"])
	require.Equal(t, "ghcr.io/cloudnative-pg/postgresql:17.9-system-trixie", pool["image"])
	require.Nil(t, pool["node_port"])
	require.Equal(t, []any{}, pool["members"], "no cluster behind the controller: no members, never null")
	require.Equal(t, []any{}, pool["databases"])

	// A project database and the platform's own on the pool, with one
	// fresh and one stale size sample for the project one.
	ctx := context.Background()
	db := dbstore.New(a.st)
	cluster, err := db.LiveClusterByName(ctx, "pg17-shared")
	require.NoError(t, err)
	projectID, environmentID := a.createEnvironment(t, token)
	project, environment := uuid.MustParse(projectID), uuid.MustParse(environmentID)
	claimRow, err := db.EnsureClaim(ctx, dbstore.ServiceOwner(project, environment, "demo", "production", "data"),
		dbstore.ClaimSpec{Engine: "postgres", Major: 17, Isolation: "shared", Availability: "single", StorageBytes: 5 << 30})
	require.NoError(t, err)
	_, err = db.BindClaim(ctx, claimRow.ID, cluster.ID)
	require.NoError(t, err)
	_, err = db.RecordTenant(ctx, dbstore.TenantInput{ClaimID: claimRow.ID, ClusterID: cluster.ID,
		DatabaseName: "demo_production_data", RoleName: "demo_production_data", CredentialSecret: "db-demo",
		Host: "pg17-shared-rw.skali-platform.svc", Port: 5432})
	require.NoError(t, err)
	_, err = db.TransitionClaim(ctx, claimRow.ID, claim.PhaseProvisioned)
	require.NoError(t, err)
	systemClaim, err := db.EnsureClaim(ctx, dbstore.SystemOwner("object-storage/metadata"),
		dbstore.ClaimSpec{Engine: "postgres", Major: 17, Isolation: "shared", Availability: "single"})
	require.NoError(t, err)
	_, err = db.BindClaim(ctx, systemClaim.ID, cluster.ID)
	require.NoError(t, err)
	_, err = db.RecordTenant(ctx, dbstore.TenantInput{ClaimID: systemClaim.ID, ClusterID: cluster.ID,
		DatabaseName: "skali_objects", RoleName: "skali_objects", CredentialSecret: "db-objects",
		Host: "pg17-shared-rw.skali-platform.svc", Port: 5432})
	require.NoError(t, err)
	now := time.Now().UTC()
	for _, sample := range []struct {
		at   time.Time
		used int64
	}{{now.Add(-30 * time.Hour), 111}, {now.Add(-time.Hour), 222}} {
		_, err := a.st.InsertStorageSamples(ctx, store.InsertStorageSamplesParams{
			SampledAt:      sample.at,
			EnvironmentIds: []uuid.UUID{environment},
			ServiceKeys:    []string{"databases.data"},
			Kinds:          []string{"database"},
			UsedBytes:      []int64{sample.used},
			UsedMeasured:   []bool{true},
			CapacityBytes:  []int64{5 << 30},
		})
		require.NoError(t, err)
	}

	status, body = a.do("GET", "/v1/system/database-pools/pg17-shared", token, nil)
	require.Equal(t, http.StatusOK, status)
	databases := body["pool"].(map[string]any)["databases"].([]any)
	require.Len(t, databases, 2)
	first := databases[0].(map[string]any)
	require.Equal(t, "demo_production_data", first["database_name"])
	require.Equal(t, "service", first["owner"])
	require.Equal(t, "data", first["service_key"])
	require.Equal(t, "provisioned", first["phase"])
	require.EqualValues(t, 5<<30, first["storage_bytes"])
	require.EqualValues(t, 222, first["used_bytes"], "the newest sample within a day")
	require.Equal(t, map[string]any{"id": projectID, "name": "demo", "display_name": ""}, first["project"])
	require.Equal(t, map[string]any{"id": environmentID, "name": "production"}, first["environment"])
	second := databases[1].(map[string]any)
	require.Equal(t, "skali_objects", second["database_name"])
	require.Equal(t, "system", second["owner"])
	require.Equal(t, "object-storage/metadata", second["system_key"])
	require.Nil(t, second["project"])
	require.Nil(t, second["environment"])
	require.Nil(t, second["used_bytes"])

	// Released pools are gone.
	_, err = db.TransitionCluster(ctx, cluster.ID, "releasing")
	require.NoError(t, err)
	_, err = db.TransitionCluster(ctx, cluster.ID, "released")
	require.NoError(t, err)
	status, _ = a.do("GET", "/v1/system/database-pools/pg17-shared", token, nil)
	require.Equal(t, http.StatusNotFound, status)
}

func TestDatabasePoolGetMembers(t *testing.T) {
	a := poolsTestAPIWithCluster(t,
		instancePod("pg17-shared-2", "pg17-shared", cnpg.RoleReplica, "db-2", false, 3),
		instancePod("pg17-shared-1", "pg17-shared", cnpg.RolePrimary, "db-1", true, 0),
		instancePod("pg18-shared-1", "pg18-shared", cnpg.RolePrimary, "db-1", true, 0),
	)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	token := a.login("admin@example.com", "hunter2hunter2")
	seedPool(t, a, "pg17-shared", pgtune.ClassShared)

	status, body := a.do("GET", "/v1/system/database-pools/pg17-shared", token, nil)
	require.Equal(t, http.StatusOK, status)
	members := body["pool"].(map[string]any)["members"].([]any)
	require.Len(t, members, 2, "the other pool's instance is not a member")
	primary := members[0].(map[string]any)
	require.Equal(t, "pg17-shared-1", primary["name"])
	require.Equal(t, "primary", primary["role"])
	require.Equal(t, "db-1", primary["node"])
	require.Equal(t, true, primary["ready"])
	require.EqualValues(t, 0, primary["restarts"])
	require.Equal(t, "Running", primary["phase"])
	replica := members[1].(map[string]any)
	require.Equal(t, "pg17-shared-2", replica["name"])
	require.Equal(t, "replica", replica["role"])
	require.Equal(t, false, replica["ready"])
	require.EqualValues(t, 3, replica["restarts"])
}

func TestDatabasePoolMetrics(t *testing.T) {
	a, _ := poolsTestAPI(t)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	token := a.login("admin@example.com", "hunter2hunter2")
	path := "/v1/system/database-pools/pg17-shared/metrics"

	status, _ := a.do("GET", path+"?window=2h", token, nil)
	require.Equal(t, http.StatusBadRequest, status)
	status, _ = a.do("GET", path, token, nil)
	require.Equal(t, http.StatusNotFound, status)

	seedPool(t, a, "pg17-shared", pgtune.ClassShared)
	status, body := a.do("GET", path+"?window=1h", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "pg17-shared", body["pool"])
	require.Equal(t, "1h", body["window"])
	require.EqualValues(t, 60, body["step_seconds"])
	require.Len(t, body["timestamps"], 60)
	require.Len(t, body["connections"], 60)
	require.Nil(t, body["current"], "no sample yet")

	ctx := context.Background()
	cluster, err := dbstore.New(a.st).LiveClusterByName(ctx, "pg17-shared")
	require.NoError(t, err)
	ptr := func(v int64) *int64 { return &v }
	// Sampled right now, so it always lands in the trailing (in-progress)
	// bucket whatever the wall clock.
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, a.st.InsertPoolMetricSample(ctx, store.InsertPoolMetricSampleParams{
		ClusterID: cluster.ID, SampledAt: now, CpuMillicores: 250, MemoryBytes: 900 << 20,
		Instances: 2, InstancesReady: 2, Connections: ptr(12), XactCommit: ptr(40), XactRollback: ptr(1),
		BlksHit: ptr(300), BlksRead: ptr(100), DatabaseBytes: ptr(70 << 20),
	}))
	seedDatabaseNode(a, "db-1", 7782<<20)

	status, body = a.do("GET", path+"?window=1h", token, nil)
	require.Equal(t, http.StatusOK, status)
	last := len(body["timestamps"].([]any)) - 1
	require.EqualValues(t, 250, body["cpu_millicores"].([]any)[last])
	require.EqualValues(t, 12, body["connections"].([]any)[last])
	require.EqualValues(t, 40, body["commits"].([]any)[last])
	require.InDelta(t, 0.75, body["cache_hit_ratio"].([]any)[last], 0.0001)
	require.Nil(t, body["connections"].([]any)[0])
	current := body["current"].(map[string]any)
	require.EqualValues(t, 250, current["cpu_millicores"])
	require.EqualValues(t, 12, current["connections"])
	require.EqualValues(t, 2, current["instances_ready"])
	require.EqualValues(t, 3328<<20, current["memory_budget_bytes"], "the shared pool's automatic budget on that node")
	require.EqualValues(t, 100, current["max_connections"])
}
