package engine_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/engine"
	"github.com/Hinkolas/skali/internal/engine/enginetest"
)

// scriptedStats returns successive RawStats readings per call, advancing 10s
// of wall clock and fixed counter increments each time.
func scriptedStats() func(id string) (engine.RawStats, error) {
	var mu sync.Mutex
	var n uint64
	base := time.Now()
	return func(string) (engine.RawStats, error) {
		mu.Lock()
		defer mu.Unlock()
		n++
		return engine.RawStats{
			At:         base.Add(time.Duration(n) * 10 * time.Second),
			CPUTotalNs: n * 2e9, // 2 cpu-seconds per 10s tick = 20%
			OnlineCPUs: 4,
			MemUsed:    100, MemLimit: 200,
			NetRx: n * 5_000, NetTx: n * 1_000,
		}, nil
	}
}

func TestSamplerDerivesStatsAfterTwoSamples(t *testing.T) {
	fake := enginetest.New("nginx:alpine")
	fake.StatsFn = scriptedStats()
	id, err := fake.Create(t.Context(), engine.ContainerSpec{Name: "web", Image: "nginx:alpine"})
	require.NoError(t, err)
	require.NoError(t, fake.Start(t.Context(), id))

	s := engine.NewSampler(fake)

	_, ok := s.Latest()
	require.False(t, ok, "cold sampler must report unknown")

	s.SampleNow(t.Context())
	obs, ok := s.Latest()
	require.True(t, ok, "one listing is enough to know what runs")
	require.Len(t, obs, 1)
	require.Equal(t, "web", obs[0].Name)
	require.Equal(t, "running", obs[0].State)
	require.Nil(t, obs[0].Stats, "rates need two samples")

	s.SampleNow(t.Context())
	obs, ok = s.Latest()
	require.True(t, ok)
	require.NotNil(t, obs[0].Stats)
	require.InDelta(t, 20.0, obs[0].Stats.CPUPercent, 0.01)
	require.Equal(t, uint64(500), obs[0].Stats.NetRxRate)
	require.Equal(t, uint64(100), obs[0].Stats.NetTxRate)
	require.Equal(t, uint64(100), obs[0].Stats.MemUsed)
}

func TestSamplerUnreachableEngineMeansUnknown(t *testing.T) {
	fake := enginetest.New("nginx:alpine")
	id, err := fake.Create(t.Context(), engine.ContainerSpec{Name: "web", Image: "nginx:alpine"})
	require.NoError(t, err)
	require.NoError(t, fake.Start(t.Context(), id))

	s := engine.NewSampler(fake)
	s.SampleNow(t.Context())
	_, ok := s.Latest()
	require.True(t, ok)

	// Engine goes away: previous observations must stop being reported.
	fake.ListErr = errors.New("cannot connect to the Docker daemon")
	s.SampleNow(t.Context())
	_, ok = s.Latest()
	require.False(t, ok, "unreachable engine is unknown, not empty")

	fake.ListErr = nil
	s.SampleNow(t.Context())
	obs, ok := s.Latest()
	require.True(t, ok)
	require.Len(t, obs, 1)
}

func TestSamplerTracksRemovalsAndIgnoresUnmanaged(t *testing.T) {
	fake := enginetest.New("nginx:alpine")
	id, err := fake.Create(t.Context(), engine.ContainerSpec{Name: "web", Image: "nginx:alpine"})
	require.NoError(t, err)
	fake.Add(engine.Container{Name: "not-ours", Image: "mysql", State: "running"})

	s := engine.NewSampler(fake)
	s.SampleNow(t.Context())
	obs, ok := s.Latest()
	require.True(t, ok)
	require.Len(t, obs, 1, "unlabeled containers are invisible")
	require.Equal(t, "web", obs[0].Name)

	require.NoError(t, fake.Remove(t.Context(), id, true))
	s.SampleNow(t.Context())
	obs, ok = s.Latest()
	require.True(t, ok)
	require.Empty(t, obs)
}
