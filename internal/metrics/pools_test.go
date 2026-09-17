package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/testdb"
)

func TestParsePoolExporter(t *testing.T) {
	t.Parallel()
	body := []byte(`# TYPE cnpg_backends_total gauge
cnpg_backends_total{datname="app",usename="app",application_name="",state="active"} 3
cnpg_backends_total{datname="app",usename="app",application_name="",state="idle"} 4
cnpg_backends_total{datname="postgres",usename="postgres",application_name="",state="idle"} 1
# TYPE cnpg_pg_stat_database_xact_commit counter
cnpg_pg_stat_database_xact_commit{datname="app"} 1000
cnpg_pg_stat_database_xact_commit{datname="postgres"} 20
# TYPE cnpg_pg_stat_database_xact_rollback counter
cnpg_pg_stat_database_xact_rollback{datname="app"} 5
# TYPE cnpg_pg_stat_database_blks_hit counter
cnpg_pg_stat_database_blks_hit{datname="app"} 9000
cnpg_pg_stat_database_blks_hit{datname="postgres"} 1000
# TYPE cnpg_pg_stat_database_blks_read counter
cnpg_pg_stat_database_blks_read{datname="app"} 100
# TYPE cnpg_pg_database_size_bytes gauge
cnpg_pg_database_size_bytes{datname="app"} 4.4646863e+07
cnpg_pg_database_size_bytes{datname="postgres"} 7.877091e+06
# TYPE cnpg_collector_up gauge
cnpg_collector_up{cluster="pg17-shared"} 1
`)
	reading, err := parsePoolExporter(body)
	require.NoError(t, err)
	require.Equal(t, poolExporterReading{
		connections:   8,
		counters:      poolCounters{commit: 1020, rollback: 5, blksHit: 10000, blksRead: 100},
		databaseBytes: 44646863 + 7877091,
	}, reading)

	empty, err := parsePoolExporter([]byte("# nothing here\n"))
	require.NoError(t, err)
	require.Equal(t, poolExporterReading{}, empty)
}

func TestPoolCountersDelta(t *testing.T) {
	t.Parallel()
	prev := poolCounters{commit: 100, rollback: 2, blksHit: 500, blksRead: 10}
	delta, ok := poolCounters{commit: 130, rollback: 2, blksHit: 800, blksRead: 12}.delta(prev)
	require.True(t, ok)
	require.Equal(t, poolCounters{commit: 30, rollback: 0, blksHit: 300, blksRead: 2}, delta)

	// A reset (failover, pg_stat_reset) shows as a counter going backwards.
	_, ok = poolCounters{commit: 5, rollback: 2, blksHit: 800, blksRead: 12}.delta(prev)
	require.False(t, ok)
}

func TestPoolUsageByCluster(t *testing.T) {
	t.Parallel()
	usage := poolUsageByCluster([]unstructuredItem{
		{labels: map[string]string{"cnpg.io/cluster": "pg17-shared"}, cpu: 10, mem: 100},
		{labels: map[string]string{"cnpg.io/cluster": "pg17-shared"}, cpu: 15, mem: 120},
		{labels: map[string]string{"cnpg.io/cluster": "pg18-shared"}, cpu: 1, mem: 2},
		{labels: map[string]string{}, cpu: 99, mem: 99},
	})
	require.Len(t, usage, 2)
	require.Equal(t, &poolPodUsage{cpu: 25, mem: 220, pods: 2}, usage["pg17-shared"])
	require.Equal(t, &poolPodUsage{cpu: 1, mem: 2, pods: 1}, usage["pg18-shared"])
}

func TestPoolSeriesAndCurrent(t *testing.T) {
	ctx := context.Background()
	st := store.NewStore(testdb.New(t))
	pool, err := dbstore.New(st).CreateCluster(ctx, dbstore.ClusterInput{
		Name: "pg17-shared", Engine: "postgres", Major: 17, Class: "shared",
		Instances: 2, StorageBytes: 10 << 30, Image: "ghcr.io/cloudnative-pg/postgresql:17.9-system-trixie",
	})
	require.NoError(t, err)
	svc := &Service{Store: st}
	now := time.Date(2026, 9, 17, 12, 0, 30, 0, time.UTC)

	ptr := func(v int64) *int64 { return &v }
	// Three ticks: the first without an exporter reading (scrape failed)
	// sits in the bucket before the trailing one (minute steps, now is 30s
	// into the trailing bucket); the next two, with counters, land in it.
	samples := []store.InsertPoolMetricSampleParams{
		{ClusterID: pool.ID, SampledAt: now.Add(-90 * time.Second), CpuMillicores: 100, MemoryBytes: 1000, Instances: 2, InstancesReady: 1},
		{ClusterID: pool.ID, SampledAt: now.Add(-20 * time.Second), CpuMillicores: 200, MemoryBytes: 2000, Instances: 2, InstancesReady: 2,
			Connections: ptr(10), XactCommit: ptr(30), XactRollback: ptr(0), BlksHit: ptr(900), BlksRead: ptr(100), DatabaseBytes: ptr(5000)},
		{ClusterID: pool.ID, SampledAt: now.Add(-10 * time.Second), CpuMillicores: 400, MemoryBytes: 4000, Instances: 2, InstancesReady: 2,
			Connections: ptr(20), XactCommit: ptr(50), XactRollback: ptr(2), BlksHit: ptr(700), BlksRead: ptr(300), DatabaseBytes: ptr(6000)},
	}
	for _, sample := range samples {
		require.NoError(t, st.InsertPoolMetricSample(ctx, sample))
	}

	window, err := WindowByName("1h")
	require.NoError(t, err)
	series, err := svc.PoolSeries(ctx, pool.ID, window, now)
	require.NoError(t, err)
	n := len(series.Timestamps)
	require.Equal(t, 60, n)
	last, before := n-1, n-2

	require.EqualValues(t, 300, *series.CPUMillicores[last])
	require.EqualValues(t, 3000, *series.MemoryBytes[last])
	require.EqualValues(t, 15, *series.Connections[last])
	require.EqualValues(t, 80, *series.Commits[last])
	require.EqualValues(t, 2, *series.Rollbacks[last])
	require.EqualValues(t, 6000, *series.DatabaseBytes[last])
	require.EqualValues(t, 2, *series.InstancesReady[last])
	require.InDelta(t, 0.8, *series.CacheHitRatio[last], 0.0001)

	// The bucket with the exporter-less sample keeps its pod side and gaps
	// the exporter series.
	require.EqualValues(t, 100, *series.CPUMillicores[before])
	require.EqualValues(t, 1, *series.InstancesReady[before])
	require.Nil(t, series.Connections[before])
	require.Nil(t, series.Commits[before])
	require.Nil(t, series.CacheHitRatio[before])
	require.Nil(t, series.DatabaseBytes[before])
	require.Nil(t, series.CPUMillicores[0])

	current, err := svc.PoolCurrent(ctx, pool.ID, now)
	require.NoError(t, err)
	require.NotNil(t, current)
	require.EqualValues(t, 400, current.CPUMillicores)
	require.EqualValues(t, 20, *current.Connections)
	require.EqualValues(t, 6000, *current.DatabaseBytes)
	require.EqualValues(t, 2, current.InstancesReady)

	// Beyond the cutoff the newest sample no longer counts as current.
	current, err = svc.PoolCurrent(ctx, pool.ID, now.Add(time.Hour))
	require.NoError(t, err)
	require.Nil(t, current)
}
