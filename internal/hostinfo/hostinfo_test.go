package hostinfo

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDeriveHostCPUAndRates(t *testing.T) {
	t0 := time.Now()
	prev := counters{
		at:      t0,
		cpuBusy: 100, cpuTotal: 1000,
		netRx: 1_000, netTx: 500,
		diskRead: 4_096, diskWrite: 0,
	}
	cur := counters{
		at:      t0.Add(10 * time.Second),
		cpuBusy: 104, cpuTotal: 1010, // 4s busy of 10s total = 40%
		memUsed: 512, memTotal: 1024,
		diskUsed: 10, diskTotal: 100,
		netRx: 11_000, netTx: 500, // +10000B over 10s = 1000B/s
		diskRead: 4_096, diskWrite: 40_960, // +40960B/10s = 4096B/s
		load1: 1.5,
	}

	snap := derive(prev, cur)
	require.InDelta(t, 40.0, snap.CPUPercent, 0.01)
	require.Equal(t, uint64(1000), snap.NetRxRate)
	require.Equal(t, uint64(0), snap.NetTxRate)
	require.Equal(t, uint64(0), snap.DiskReadRate)
	require.Equal(t, uint64(4096), snap.DiskWriteRate)
	require.Equal(t, uint64(512), snap.MemUsed)
	require.Equal(t, uint64(1024), snap.MemTotal)
	require.InDelta(t, 1.5, snap.Load1, 0.001)
	require.Equal(t, cur.at, snap.SampledAt)
}

func TestDeriveCgroupCPU(t *testing.T) {
	t0 := time.Now()
	// 2 effective cores; 5s of CPU time over 10s wall = 25% of the quota.
	prev := counters{at: t0, cgroupCPU: true, cpuUsec: 1_000_000, cores: 2}
	cur := counters{at: t0.Add(10 * time.Second), cgroupCPU: true, cpuUsec: 6_000_000, cores: 2}
	require.InDelta(t, 25.0, derive(prev, cur).CPUPercent, 0.01)

	// Saturated beyond the quota clamps at 100.
	cur.cpuUsec = 1_000_000 + 30_000_000
	require.InDelta(t, 100.0, derive(prev, cur).CPUPercent, 0.01)
}

func TestDeriveCounterResetYieldsZeroRate(t *testing.T) {
	t0 := time.Now()
	prev := counters{at: t0, netRx: 5_000_000}
	cur := counters{at: t0.Add(10 * time.Second), netRx: 1_000} // reboot/reset
	require.Equal(t, uint64(0), derive(prev, cur).NetRxRate)
}

func TestSamplerLatestNotReadyUntilTwoSamples(t *testing.T) {
	s := New(t.TempDir())
	_, ok := s.Latest()
	require.False(t, ok)

	ctx := context.Background()
	s.SampleNow(ctx)
	_, ok = s.Latest()
	require.False(t, ok, "one sample cannot produce rates")

	s.SampleNow(ctx)
	snap, ok := s.Latest()
	require.True(t, ok)
	require.NotZero(t, snap.SampledAt)
	// Real readings on any OS: totals present, percent within range.
	require.Positive(t, snap.MemTotal)
	require.Positive(t, snap.DiskTotal)
	require.GreaterOrEqual(t, snap.CPUPercent, 0.0)
	require.LessOrEqual(t, snap.CPUPercent, 100.0)
}

// --- cgroup parsing against a fake root ---

func fakeCgroup(t *testing.T, files map[string]string) cgroup {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	}
	return cgroup{root: dir}
}

func TestCgroupCPULimit(t *testing.T) {
	c := fakeCgroup(t, map[string]string{"cpu.max": "200000 100000\n"})
	cores, ok := c.cpuLimit()
	require.True(t, ok)
	require.InDelta(t, 2.0, cores, 0.001)

	for name, content := range map[string]string{
		"unlimited": "max 100000\n",
		"malformed": "banana\n",
		"empty":     "",
	} {
		c := fakeCgroup(t, map[string]string{"cpu.max": content})
		_, ok := c.cpuLimit()
		require.False(t, ok, name)
	}

	// Missing file entirely (not a cgroup v2 host).
	_, ok = cgroup{root: t.TempDir()}.cpuLimit()
	require.False(t, ok)
}

func TestCgroupCPUUsage(t *testing.T) {
	c := fakeCgroup(t, map[string]string{
		"cpu.stat": "usage_usec 123456\nuser_usec 100000\nsystem_usec 23456\n",
	})
	v, ok := c.cpuUsageUsec()
	require.True(t, ok)
	require.Equal(t, uint64(123456), v)
}

func TestCgroupMemory(t *testing.T) {
	c := fakeCgroup(t, map[string]string{
		"memory.max":     "1073741824\n",
		"memory.current": "536870912\n",
		"memory.stat":    "anon 400000000\ninactive_file 36870912\nactive_file 100000000\n",
	})
	limit, ok := c.memLimit()
	require.True(t, ok)
	require.Equal(t, uint64(1073741824), limit)

	// Working set = current - inactive_file.
	used, ok := c.memUsed()
	require.True(t, ok)
	require.Equal(t, uint64(536870912-36870912), used)

	unlimited := fakeCgroup(t, map[string]string{"memory.max": "max\n"})
	_, ok = unlimited.memLimit()
	require.False(t, ok)
}

func TestCgroupIOBytes(t *testing.T) {
	c := fakeCgroup(t, map[string]string{
		"io.stat": "259:0 rbytes=1000 wbytes=2000 rios=10 wios=20\n253:0 rbytes=500 wbytes=250 rios=5 wios=2\n",
	})
	r, w, ok := c.ioBytes()
	require.True(t, ok)
	require.Equal(t, uint64(1500), r)
	require.Equal(t, uint64(2250), w)

	_, _, ok = cgroup{root: t.TempDir()}.ioBytes()
	require.False(t, ok)
}

func TestPartitionFilter(t *testing.T) {
	partitions := []string{"sda1", "sdb12", "vda2", "nvme0n1p1", "mmcblk0p2", "xvda1"}
	wholeDisks := []string{"sda", "vda", "nvme0n1", "mmcblk0", "disk0", "dm-0"}
	virtual := []string{"loop0", "ram1", "zram0", "fd0"}

	for _, name := range partitions {
		require.True(t, partitionRe.MatchString(name), name)
	}
	for _, name := range wholeDisks {
		require.False(t, partitionRe.MatchString(name), name)
		require.False(t, virtualRe.MatchString(name), name)
	}
	for _, name := range virtual {
		require.True(t, virtualRe.MatchString(name), name)
	}
}
