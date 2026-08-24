package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/store"
)

func TestEnvironmentMetrics(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("metrics@example.com", "hunter2hunter2")
	token := a.login("metrics@example.com", "hunter2hunter2")
	_, envID := a.createEnvironment(t, token)

	// Empty until the sampler wrote anything: full grid, no applications.
	status, body := a.do("GET", "/v1/environments/"+envID+"/metrics", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "24h", body["window"])
	require.EqualValues(t, 900, body["step_seconds"])
	require.Len(t, body["timestamps"], 96)
	require.NotNil(t, body["applications"], "applications must be an empty list, not null")
	require.Empty(t, body["applications"])

	// A stored sample shows up on the aligned grid with null gaps.
	envUUID := uuid.MustParse(envID)
	_, err := a.st.InsertAppMetricSamples(context.Background(), store.InsertAppMetricSamplesParams{
		SampledAt:       time.Now().UTC(),
		EnvironmentIds:  []uuid.UUID{envUUID},
		ApplicationKeys: []string{"web"},
		CpuMillicores:   []int64{123},
		MemoryBytes:     []int64{456},
		PodCounts:       []int64{2},
	})
	require.NoError(t, err)

	status, body = a.do("GET", "/v1/environments/"+envID+"/metrics?window=1h", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "1h", body["window"])
	require.EqualValues(t, 60, body["step_seconds"])
	timestamps := body["timestamps"].([]any)
	require.Len(t, timestamps, 60)
	apps := body["applications"].([]any)
	require.Len(t, apps, 1)
	app := apps[0].(map[string]any)
	require.Equal(t, "web", app["key"])
	cpu := app["cpu_millicores"].([]any)
	require.Len(t, cpu, 60)
	// The just-written sample lands in the trailing in-progress bucket.
	require.EqualValues(t, 123, cpu[59])
	require.Nil(t, cpu[0])
	mem := app["memory_bytes"].([]any)
	require.EqualValues(t, 456, mem[59])

	// Edge samples attach an edge object to the application's entry.
	_, err = a.st.InsertEdgeMetricSamples(context.Background(), store.InsertEdgeMetricSamplesParams{
		SampledAt:       time.Now().UTC(),
		EnvironmentIds:  []uuid.UUID{envUUID},
		ApplicationKeys: []string{"web"},
		RouteKeys:       []string{"public"},
		Requests:        []int64{7},
		RequestBytes:    []int64{70},
		ResponseBytes:   []int64{700},
	})
	require.NoError(t, err)
	status, body = a.do("GET", "/v1/environments/"+envID+"/metrics?window=1h", token, nil)
	require.Equal(t, http.StatusOK, status)
	edge := body["applications"].([]any)[0].(map[string]any)["edge"].(map[string]any)
	require.EqualValues(t, 7, edge["requests"].([]any)[59])
	require.EqualValues(t, 700, edge["response_bytes"].([]any)[59])

	// Unknown windows are rejected, unknown environments are 404, and
	// unauthenticated reads are rejected.
	status, _ = a.do("GET", "/v1/environments/"+envID+"/metrics?window=2h", token, nil)
	require.Equal(t, http.StatusBadRequest, status)
	status, _ = a.do("GET", "/v1/environments/"+uuid.NewString()+"/metrics", token, nil)
	require.Equal(t, http.StatusNotFound, status)
	status, _ = a.do("GET", "/v1/environments/"+envID+"/metrics", "", nil)
	require.Equal(t, http.StatusUnauthorized, status)
}

func TestNodeMetrics(t *testing.T) {
	a := newTestAPI(t)
	a.createAdmin("nodemetrics@example.com", "hunter2hunter2")
	a.createUser("member2@example.com", "hunter2hunter2")
	admin := a.login("nodemetrics@example.com", "hunter2hunter2")

	// Instance-wide usage is admin only.
	member := a.login("member2@example.com", "hunter2hunter2")
	status, _ := a.do("GET", "/v1/nodes/metrics", member, nil)
	require.Equal(t, http.StatusForbidden, status)

	_, err := a.st.InsertNodeMetricSamples(context.Background(), store.InsertNodeMetricSamplesParams{
		SampledAt:                time.Now().UTC(),
		NodeNames:                []string{"node-a"},
		CpuMillicores:            []int64{250},
		MemoryBytes:              []int64{1 << 30},
		CpuAllocatableMillicores: []int64{4000},
		MemoryAllocatableBytes:   []int64{8 << 30},
	})
	require.NoError(t, err)

	status, body := a.do("GET", "/v1/nodes/metrics?window=1h", admin, nil)
	require.Equal(t, http.StatusOK, status)
	nodes := body["nodes"].([]any)
	require.Len(t, nodes, 1)
	node := nodes[0].(map[string]any)
	require.Equal(t, "node-a", node["name"])
	require.EqualValues(t, 4000, node["cpu_allocatable_millicores"])
	cpu := node["cpu_millicores"].([]any)
	require.Len(t, cpu, 60)
	require.EqualValues(t, 250, cpu[59])
}

func TestNodesStorage(t *testing.T) {
	a := newTestAPI(t)
	a.createAdmin("storage@example.com", "hunter2hunter2")
	a.createUser("member3@example.com", "hunter2hunter2")
	admin := a.login("storage@example.com", "hunter2hunter2")

	// Instance-wide storage is admin only.
	member := a.login("member3@example.com", "hunter2hunter2")
	status, _ := a.do("GET", "/v1/nodes/storage", member, nil)
	require.Equal(t, http.StatusForbidden, status)

	// Empty until the sampler wrote a recent sample.
	status, body := a.do("GET", "/v1/nodes/storage", admin, nil)
	require.Equal(t, http.StatusOK, status)
	require.NotNil(t, body["nodes"], "nodes must be an empty list, not null")
	require.Empty(t, body["nodes"])

	_, err := a.st.InsertStorageNodeSamples(context.Background(), store.InsertStorageNodeSamplesParams{
		SampledAt:      time.Now().UTC(),
		NodeNames:      []string{"node-a"},
		CapacityBytes:  []int64{1000},
		UsedBytes:      []int64{500},
		AvailableBytes: []int64{450},
		VolumesBytes:   []int64{100},
		DatabasesBytes: []int64{50},
		ObjectsBytes:   []int64{25},
		ImagesBytes:    []int64{125},
	})
	require.NoError(t, err)

	status, body = a.do("GET", "/v1/nodes/storage", admin, nil)
	require.Equal(t, http.StatusOK, status)
	nodes := body["nodes"].([]any)
	require.Len(t, nodes, 1)
	node := nodes[0].(map[string]any)
	require.Equal(t, "node-a", node["name"])
	require.EqualValues(t, 1000, node["capacity_bytes"])
	require.EqualValues(t, 500, node["used_bytes"])
	categories := node["categories"].(map[string]any)
	require.EqualValues(t, 100, categories["volumes_bytes"])
	// system = used minus the attributed categories.
	require.EqualValues(t, 200, categories["system_bytes"])
}

func TestProjectStorage(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("projstorage@example.com", "hunter2hunter2")
	token := a.login("projstorage@example.com", "hunter2hunter2")
	projectID, envID := a.createEnvironment(t, token)

	// Empty before any sample.
	status, body := a.do("GET", "/v1/projects/"+projectID+"/storage", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.NotNil(t, body["services"], "services must be an empty list, not null")
	require.Empty(t, body["services"])

	_, err := a.st.InsertStorageSamples(context.Background(), store.InsertStorageSamplesParams{
		SampledAt:      time.Now().UTC(),
		EnvironmentIds: []uuid.UUID{uuid.MustParse(envID), uuid.MustParse(envID)},
		ServiceKeys:    []string{"files", "buckets.media"},
		Kinds:          []string{"volume", "bucket"},
		UsedBytes:      []int64{0, 777},
		UsedMeasured:   []bool{false, true},
		CapacityBytes:  []int64{100, 1000},
	})
	require.NoError(t, err)

	status, body = a.do("GET", "/v1/projects/"+projectID+"/storage", token, nil)
	require.Equal(t, http.StatusOK, status)
	services := body["services"].([]any)
	require.Len(t, services, 2)
	byKey := map[string]map[string]any{}
	for _, entry := range services {
		service := entry.(map[string]any)
		byKey[service["service_key"].(string)] = service
	}
	require.Nil(t, byKey["files"]["used_bytes"], "unmeasured usage must serialize as null")
	require.EqualValues(t, 100, byKey["files"]["capacity_bytes"])
	require.Equal(t, "volume", byKey["files"]["kind"])
	require.EqualValues(t, 777, byKey["buckets.media"]["used_bytes"])

	// Membership gates the read: a stranger sees 404, anonymous 401.
	a.createUser("stranger@example.com", "hunter2hunter2")
	stranger := a.login("stranger@example.com", "hunter2hunter2")
	status, _ = a.do("GET", "/v1/projects/"+projectID+"/storage", stranger, nil)
	require.Contains(t, []int{http.StatusForbidden, http.StatusNotFound}, status)
	status, _ = a.do("GET", "/v1/projects/"+projectID+"/storage", "", nil)
	require.Equal(t, http.StatusUnauthorized, status)
}
