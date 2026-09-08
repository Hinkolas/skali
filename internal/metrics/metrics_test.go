package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/testdb"
)

func TestWindowByName(t *testing.T) {
	w, err := WindowByName("")
	require.NoError(t, err)
	require.Equal(t, "24h", w.Name)

	w, err = WindowByName("1h")
	require.NoError(t, err)
	require.Equal(t, time.Minute, w.Step)

	_, err = WindowByName("2h")
	require.Error(t, err)
}

func TestWindowGrid(t *testing.T) {
	w, err := WindowByName("1h")
	require.NoError(t, err)
	now := time.Date(2026, 8, 20, 12, 30, 42, 0, time.UTC)
	since, until, timestamps := w.grid(now)

	// The trailing bucket is the in-progress one.
	require.Equal(t, time.Date(2026, 8, 20, 12, 31, 0, 0, time.UTC), until)
	require.Equal(t, until.Add(-time.Hour), since)
	require.Len(t, timestamps, 60)
	require.Equal(t, since, timestamps[0])
	require.Equal(t, until.Add(-time.Minute), timestamps[59])
}

func TestPodUsage(t *testing.T) {
	item := unstructured.Unstructured{Object: map[string]any{
		"containers": []any{
			map[string]any{"name": "web", "usage": map[string]any{"cpu": "250m", "memory": "128Mi"}},
			map[string]any{"name": "sidecar", "usage": map[string]any{"cpu": "1", "memory": "1Ki"}},
		},
	}}
	cpu, mem, err := podUsage(item)
	require.NoError(t, err)
	require.Equal(t, int64(1250), cpu)
	require.Equal(t, int64(128*1024*1024+1024), mem)

	_, _, err = podUsage(unstructured.Unstructured{Object: map[string]any{
		"containers": []any{map[string]any{"usage": map[string]any{"cpu": "not-a-quantity"}}},
	}})
	require.Error(t, err)
}

// seedEnvironment creates the project and environment rows the sample FK
// requires.
func seedEnvironment(t *testing.T, st *store.Store) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	project, err := st.CreateProject(ctx, store.CreateProjectParams{
		ID: uuid.New(), Name: "demo", DisplayName: "Demo", SourceMode: "file",
	})
	require.NoError(t, err)
	environment, err := st.CreateEnvironment(ctx, store.CreateEnvironmentParams{
		ID: uuid.New(), ProjectID: project.ID, Name: "production", MaxRole: "admin", Priority: "normal",
	})
	require.NoError(t, err)
	return environment.ID
}

func TestEnvironmentSeries(t *testing.T) {
	ctx := context.Background()
	st := store.NewStore(testdb.New(t))
	envID := seedEnvironment(t, st)
	svc := &Service{Store: st}

	now := time.Date(2026, 8, 20, 12, 0, 30, 0, time.UTC)
	// Samples in three distinct buckets, gaps elsewhere.
	insert := func(at time.Time, cpu, mem int64) {
		_, err := st.InsertAppMetricSamples(ctx, store.InsertAppMetricSamplesParams{
			SampledAt:       at,
			EnvironmentIds:  []uuid.UUID{envID},
			ApplicationKeys: []string{"web"},
			CpuMillicores:   []int64{cpu},
			MemoryBytes:     []int64{mem},
			PodCounts:       []int64{1},
		})
		require.NoError(t, err)
	}
	insert(now.Add(-3*time.Minute), 100, 1000)
	insert(now.Add(-150*time.Second), 300, 3000)
	insert(now, 500, 5000)

	w, err := WindowByName("1h")
	require.NoError(t, err)
	series, err := svc.EnvironmentSeries(ctx, envID, w, now)
	require.NoError(t, err)
	require.Len(t, series.Timestamps, 60)
	require.Len(t, series.Apps, 1)

	app := series.Apps[0]
	require.Equal(t, "web", app.Key)
	require.Len(t, app.CPUMillicores, 60)

	valueAt := func(at time.Time) *int64 {
		for i, ts := range series.Timestamps {
			if ts.Equal(at.Truncate(time.Minute)) {
				return app.CPUMillicores[i]
			}
		}
		t.Fatalf("timestamp %v not on grid", at)
		return nil
	}
	require.NotNil(t, valueAt(now.Add(-3*time.Minute)))
	require.EqualValues(t, 100, *valueAt(now.Add(-3 * time.Minute)))
	// The in-progress trailing bucket carries the newest sample.
	last := app.CPUMillicores[59]
	require.NotNil(t, last)
	require.EqualValues(t, 500, *last)
	// Buckets without samples are nil.
	require.Nil(t, app.CPUMillicores[0])

	// Unknown environments read as an empty series, not an error.
	empty, err := svc.EnvironmentSeries(ctx, uuid.New(), w, now)
	require.NoError(t, err)
	require.Empty(t, empty.Apps)
}

func TestInsertSkipsUnknownEnvironment(t *testing.T) {
	ctx := context.Background()
	st := store.NewStore(testdb.New(t))
	envID := seedEnvironment(t, st)

	rows, err := st.InsertAppMetricSamples(ctx, store.InsertAppMetricSamplesParams{
		SampledAt:       time.Now().UTC(),
		EnvironmentIds:  []uuid.UUID{envID, uuid.New()},
		ApplicationKeys: []string{"web", "ghost"},
		CpuMillicores:   []int64{1, 2},
		MemoryBytes:     []int64{1, 2},
		PodCounts:       []int64{1, 1},
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)
}

func TestNodesSeries(t *testing.T) {
	ctx := context.Background()
	st := store.NewStore(testdb.New(t))
	svc := &Service{Store: st}

	now := time.Date(2026, 8, 20, 12, 0, 30, 0, time.UTC)
	_, err := st.InsertNodeMetricSamples(ctx, store.InsertNodeMetricSamplesParams{
		SampledAt:                now,
		NodeNames:                []string{"node-a", "node-b"},
		CpuMillicores:            []int64{400, 800},
		MemoryBytes:              []int64{1 << 30, 2 << 30},
		CpuAllocatableMillicores: []int64{4000, 8000},
		MemoryAllocatableBytes:   []int64{8 << 30, 16 << 30},
	})
	require.NoError(t, err)

	w, err := WindowByName("1h")
	require.NoError(t, err)
	series, err := svc.NodesSeries(ctx, w, now)
	require.NoError(t, err)
	require.Len(t, series.Nodes, 2)
	require.Equal(t, "node-a", series.Nodes[0].Name)
	require.EqualValues(t, 4000, series.Nodes[0].CPUAllocatableMillicores)
	last := series.Nodes[0].CPUMillicores[59]
	require.NotNil(t, last)
	require.EqualValues(t, 400, *last)
}

func TestPrune(t *testing.T) {
	ctx := context.Background()
	st := store.NewStore(testdb.New(t))
	envID := seedEnvironment(t, st)

	old := time.Now().UTC().Add(-9 * 24 * time.Hour)
	_, err := st.InsertAppMetricSamples(ctx, store.InsertAppMetricSamplesParams{
		SampledAt:       old,
		EnvironmentIds:  []uuid.UUID{envID},
		ApplicationKeys: []string{"web"},
		CpuMillicores:   []int64{1},
		MemoryBytes:     []int64{1},
		PodCounts:       []int64{1},
	})
	require.NoError(t, err)

	deleted, err := st.DeleteAgedAppMetricSamples(ctx, time.Now().UTC().Add(-defaultRetention))
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted)
}
