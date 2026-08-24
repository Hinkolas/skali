package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/testdb"
)

// The degenerate case is load-bearing: hostPath-backed provisioners answer
// whole-node statfs for every claim, and treating that as usage would book
// the full disk onto every app.
func TestParseStatsSummary(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"node": {
			"nodeName": "cp-1",
			"fs": {"capacityBytes": 500000000000, "usedBytes": 40000000000, "availableBytes": 435000000000},
			"runtime": {"imageFs": {"capacityBytes": 500000000000, "usedBytes": 9000000000, "availableBytes": 435000000000}}
		},
		"pods": [
			{"volume": [
				{"capacityBytes": 10000000000, "usedBytes": 1234567, "availableBytes": 9998765433,
				 "pvcRef": {"name": "files-data", "namespace": "skali-demo-production"}},
				{"capacityBytes": 500000000000, "usedBytes": 40000000000, "availableBytes": 435000000000,
				 "pvcRef": {"name": "seaweed-data", "namespace": "skali-platform"}},
				{"capacityBytes": 1000000, "usedBytes": 4096}
			]},
			{"volume": [
				{"capacityBytes": 10000000000, "usedBytes": 7654321, "availableBytes": 9992345679,
				 "pvcRef": {"name": "files-data", "namespace": "skali-demo-production"}}
			]}
		]
	}`)
	summary, err := parseStatsSummary(body)
	require.NoError(t, err)
	require.Equal(t, int64(500000000000), summary.capacityBytes)
	require.Equal(t, int64(40000000000), summary.usedBytes)
	require.Equal(t, int64(435000000000), summary.availableBytes)
	require.Equal(t, int64(9000000000), summary.imageFsUsedBytes)
	// The CSI-shaped claim passes and sums across mounts; the degenerate
	// claim (capacity equals node fs) and the ref-less volume are dropped.
	require.Equal(t, map[pvcRef]int64{
		{namespace: "skali-demo-production", name: "files-data"}: 1234567 + 7654321,
	}, summary.volumes)

	_, err = parseStatsSummary([]byte(`{"pods": []}`))
	require.Error(t, err, "a summary without node fs stats is unusable")
}

func TestParseGaugeByLabel(t *testing.T) {
	t.Parallel()
	body := []byte(`# HELP cnpg_pg_database_size_bytes Disk space used by the database
# TYPE cnpg_pg_database_size_bytes gauge
cnpg_pg_database_size_bytes{datname="app_data_26a8b552"} 4.4646863e+07
cnpg_pg_database_size_bytes{datname="postgres"} 7.877091e+06
cnpg_pg_database_size_bytes{datname="template1"} 7.729635e+06
# TYPE cnpg_collector_up gauge
cnpg_collector_up{cluster="pg17-shared"} 1
`)
	sizes, err := parseGaugeByLabel(body, databaseSizeFamily, "datname")
	require.NoError(t, err)
	require.Equal(t, map[string]int64{
		"app_data_26a8b552": 44646863,
		"postgres":          7877091,
		"template1":         7729635,
	}, sizes)
}

func TestStorageReads(t *testing.T) {
	ctx := context.Background()
	st := store.NewStore(testdb.New(t))
	envID := seedEnvironment(t, st)
	projectID := projectOf(t, st, envID)
	svc := &Service{Store: st}
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	older := now.Add(-10 * time.Minute)
	newer := now.Add(-5 * time.Minute)
	stale := now.Add(-30 * time.Hour)
	for _, at := range []time.Time{stale, older, newer} {
		used := at.Unix()
		_, err := st.InsertStorageNodeSamples(ctx, store.InsertStorageNodeSamplesParams{
			SampledAt:      at,
			NodeNames:      []string{"cp-1"},
			CapacityBytes:  []int64{500},
			UsedBytes:      []int64{used},
			AvailableBytes: []int64{500 - used},
			VolumesBytes:   []int64{1},
			DatabasesBytes: []int64{2},
			ObjectsBytes:   []int64{3},
			ImagesBytes:    []int64{4},
		})
		require.NoError(t, err)
		_, err = st.InsertStorageSamples(ctx, store.InsertStorageSamplesParams{
			SampledAt:      at,
			EnvironmentIds: []uuid.UUID{envID, envID},
			ServiceKeys:    []string{"files", "databases.data"},
			Kinds:          []string{"volume", "database"},
			UsedBytes:      []int64{0, used},
			UsedMeasured:   []bool{false, true},
			CapacityBytes:  []int64{100, 200},
		})
		require.NoError(t, err)
	}

	nodes, err := svc.NodesStorage(ctx, now)
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	require.Equal(t, "cp-1", nodes[0].Name)
	require.Equal(t, newer.Unix(), nodes[0].UsedBytes, "DISTINCT ON must return the newest sample")

	services, err := svc.ProjectStorage(ctx, projectID, now)
	require.NoError(t, err)
	require.Len(t, services, 2)
	byKey := map[string]ServiceStorage{}
	for _, service := range services {
		byKey[service.ServiceKey] = service
	}
	require.Nil(t, byKey["files"].UsedBytes, "unmeasured usage must round-trip as null")
	require.Equal(t, int64(100), byKey["files"].CapacityBytes)
	require.NotNil(t, byKey["databases.data"].UsedBytes)
	require.Equal(t, newer.Unix(), *byKey["databases.data"].UsedBytes)
	require.True(t, newer.Equal(byKey["databases.data"].SampledAt),
		"the newest sample's timestamp must round-trip (same instant)")

	// Only stale rows: the cutoffs make both reads empty.
	empty, err := svc.NodesStorage(ctx, stale.Add(48*time.Hour))
	require.NoError(t, err)
	require.Empty(t, empty)
	emptyServices, err := svc.ProjectStorage(ctx, projectID, stale.Add(72*time.Hour))
	require.NoError(t, err)
	require.Empty(t, emptyServices)

	// A batch for an unknown environment is skipped, not an FK failure.
	rows, err := st.InsertStorageSamples(ctx, store.InsertStorageSamplesParams{
		SampledAt:      now,
		EnvironmentIds: []uuid.UUID{uuid.New()},
		ServiceKeys:    []string{"ghost"},
		Kinds:          []string{"volume"},
		UsedBytes:      []int64{1},
		UsedMeasured:   []bool{true},
		CapacityBytes:  []int64{1},
	})
	require.NoError(t, err)
	require.Zero(t, rows)
}

// projectOf reads the environment's project id back for the project-scoped
// query.
func projectOf(t *testing.T, st *store.Store, envID uuid.UUID) uuid.UUID {
	t.Helper()
	environment, err := st.GetEnvironmentByID(context.Background(), envID)
	require.NoError(t, err)
	return environment.ProjectID
}
