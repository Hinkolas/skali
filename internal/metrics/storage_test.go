package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	rendering "github.com/Hinkolas/skali/internal/kubernetes"
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
			{"podRef": {"name": "files-6f7d8", "namespace": "skali-demo-production"},
			 "ephemeral-storage": {"capacityBytes": 500000000000, "usedBytes": 123456, "availableBytes": 435000000000},
			 "volume": [
				{"capacityBytes": 10000000000, "usedBytes": 1234567, "availableBytes": 9998765433,
				 "pvcRef": {"name": "files-data", "namespace": "skali-demo-production"}},
				{"capacityBytes": 500000000000, "usedBytes": 40000000000, "availableBytes": 435000000000,
				 "pvcRef": {"name": "seaweed-data", "namespace": "skali-platform"}},
				{"capacityBytes": 1000000, "usedBytes": 4096}
			]},
			{"podRef": {"name": "traefik-abc12", "namespace": "kube-system"},
			 "ephemeral-storage": {"capacityBytes": 500000000000, "usedBytes": 900000, "availableBytes": 435000000000}},
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
	// Temporary (ephemeral) usage: the node total counts every pod, system
	// pods included; the per-pod map keys carry it into the service join.
	// Unlike per-claim numbers there is no degenerate filter: the reported
	// capacity is the node filesystem's by design.
	require.Equal(t, int64(123456+900000), summary.temporaryBytes)
	require.Equal(t, map[podKey]int64{
		{namespace: "skali-demo-production", name: "files-6f7d8"}: 123456,
		{namespace: "kube-system", name: "traefik-abc12"}:         900000,
	}, summary.pods)

	_, err = parseStatsSummary([]byte(`{"pods": []}`))
	require.Error(t, err, "a summary without node fs stats is unusable")
}

// The join is where attribution can go wrong: labels decide the row, the
// declared limit is the capacity, and pods without a kubelet reading must
// not fake a measurement.
func TestTemporaryRows(t *testing.T) {
	t.Parallel()
	envA := uuid.New()
	limit := resource.MustParse("2Gi")
	pod := func(namespace, name, env, service string, limited bool) corev1.Pod {
		p := corev1.Pod{}
		p.Namespace, p.Name = namespace, name
		p.Labels = map[string]string{
			rendering.LabelEnvironment: env,
			rendering.LabelService:     service,
		}
		p.Spec.Containers = []corev1.Container{{Name: "app"}}
		if limited {
			p.Spec.Containers[0].Resources.Limits = corev1.ResourceList{
				corev1.ResourceEphemeralStorage: limit,
			}
		}
		return p
	}
	pods := []corev1.Pod{
		// Two pods of one service (mid-rollout): usage and limits both sum.
		pod("skali-demo-production", "files-1", envA.String(), "files", true),
		pod("skali-demo-production", "files-2", envA.String(), "files", true),
		// Measured but no declared limit: still a row, capacity 0.
		pod("skali-demo-production", "web-1", envA.String(), "web", false),
		// No reading and no limit: nothing to say, no row.
		pod("skali-demo-production", "idle-1", envA.String(), "idle", false),
		// Release-command pods and unlabeled pods never become rows.
		pod("skali-demo-production", "files-release", envA.String(),
			rendering.ReleaseServiceIdentity("files"), true),
		pod("skali-platform", "skalid-1", "not-a-uuid", "skalid", false),
	}
	readings := map[podKey]int64{
		{namespace: "skali-demo-production", name: "files-1"}:       1000,
		{namespace: "skali-demo-production", name: "files-2"}:       200,
		{namespace: "skali-demo-production", name: "web-1"}:         50,
		{namespace: "skali-demo-production", name: "files-release"}: 7,
	}
	rows := temporaryRows(pods, readings)
	require.Len(t, rows, 2)
	byService := map[string]serviceStorage{}
	for _, row := range rows {
		require.Equal(t, kindTemporary, row.kind)
		require.Equal(t, envA, row.environment)
		byService[row.serviceKey] = row
	}
	require.NotNil(t, byService["files"].used)
	require.Equal(t, int64(1200), *byService["files"].used)
	require.Equal(t, 2*limit.Value(), byService["files"].capacity)
	require.NotNil(t, byService["web"].used)
	require.Equal(t, int64(50), *byService["web"].used)
	require.Zero(t, byService["web"].capacity)

	// A declared limit without a reading (kubelet missed the interval)
	// still reports the reservation, with usage honestly null.
	rows = temporaryRows(pods[:1], map[podKey]int64{})
	require.Len(t, rows, 1)
	require.Nil(t, rows[0].used)
	require.Equal(t, limit.Value(), rows[0].capacity)
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
			TemporaryBytes: []int64{5},
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
	require.Equal(t, int64(5), nodes[0].TemporaryBytes)

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
