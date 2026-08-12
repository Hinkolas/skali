package devports

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func always(int) bool { return true }

func TestAllocateDeterministic(t *testing.T) {
	t.Parallel()
	first, err := New(20000, 5000, nil, always).
		Allocate("guestbook", "web", []string{"web", "metrics"}, nil)
	require.NoError(t, err)
	second, err := New(20000, 5000, nil, always).
		Allocate("guestbook", "web", []string{"metrics", "web"}, nil)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Len(t, first, 2)
	for _, port := range first {
		require.GreaterOrEqual(t, port, 20000)
		require.Less(t, port, 25000)
	}
	require.NotEqual(t, first["web"], first["metrics"])
}

func TestAllocatePinsVerbatim(t *testing.T) {
	t.Parallel()
	allocated, err := New(20000, 5000, nil, always).
		Allocate("guestbook", "web", []string{"web", "metrics"}, map[string]int{"web": 5173})
	require.NoError(t, err)
	require.Equal(t, 5173, allocated["web"])
}

func TestAllocatePinnedBusy(t *testing.T) {
	t.Parallel()
	probe := func(port int) bool { return port != 5173 }
	_, err := New(20000, 5000, nil, probe).
		Allocate("guestbook", "web", []string{"web"}, map[string]int{"web": 5173})
	require.ErrorContains(t, err, "already in use")
	require.ErrorContains(t, err, "5173")
	require.ErrorContains(t, err, "web")
}

func TestAllocateProbesPastBusyAndReserved(t *testing.T) {
	t.Parallel()
	reference, err := New(20000, 5000, nil, always).
		Allocate("guestbook", "web", []string{"web"}, nil)
	require.NoError(t, err)
	candidate := reference["web"]

	// The hash candidate is busy and its successor is reserved: allocation
	// lands two above, deterministically.
	probe := func(port int) bool { return port != candidate }
	allocated, err := New(20000, 5000, []int{candidate + 1}, probe).
		Allocate("guestbook", "web", []string{"web"}, nil)
	require.NoError(t, err)
	require.Equal(t, candidate+2, allocated["web"])
}

func TestAllocateNeverReusesWithinSession(t *testing.T) {
	t.Parallel()
	// A two-slot range forces both applications through the same
	// candidates; the session-wide taken set keeps them apart.
	allocator := New(20000, 2, nil, always)
	first, err := allocator.Allocate("guestbook", "web", []string{"web"}, nil)
	require.NoError(t, err)
	second, err := allocator.Allocate("guestbook", "api", []string{"web"}, nil)
	require.NoError(t, err)
	require.NotEqual(t, first["web"], second["web"])
}

func TestAllocatePinBlocksAuto(t *testing.T) {
	t.Parallel()
	allocator := New(20000, 1, nil, always)
	allocated, err := allocator.Allocate("guestbook", "web",
		[]string{"web", "metrics"}, map[string]int{"web": 20000})
	require.ErrorContains(t, err, "no free dev port")
	require.Nil(t, allocated)
}

func TestAllocateWraparound(t *testing.T) {
	t.Parallel()
	// Only the range's first port is free: whatever the candidate, probing
	// wraps around to it.
	probe := func(port int) bool { return port == 20000 }
	allocated, err := New(20000, 5000, nil, probe).
		Allocate("guestbook", "web", []string{"web"}, nil)
	require.NoError(t, err)
	require.Equal(t, 20000, allocated["web"])
}

func TestAllocateExhaustion(t *testing.T) {
	t.Parallel()
	_, err := New(20000, 5000, nil, func(int) bool { return false }).
		Allocate("guestbook", "web", []string{"web"}, nil)
	require.ErrorContains(t, err, "no free dev port in range 20000-24999")
	require.ErrorContains(t, err, "SKALI_DEV_PORT_BASE")
}

func TestDefaultBaseOverride(t *testing.T) {
	t.Setenv("SKALI_DEV_PORT_BASE", "30000")
	allocator := Default()
	require.Equal(t, 30000, allocator.base)

	t.Setenv("SKALI_DEV_PORT_BASE", "nonsense")
	require.Equal(t, DefaultBase, Default().base)
}
