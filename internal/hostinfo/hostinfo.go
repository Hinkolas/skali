// Package hostinfo samples host resource metrics — CPU utilization, memory,
// disk usage, network and disk-I/O rates — on every node. It is
// cgroup-v2-aware: inside a resource-limited container (the DinD dev cluster,
// or any containerized skalid) usage and totals are read from the container's
// own cgroup instead of the host's /proc, so each node reports honest values.
//
// CPU, network, and disk-I/O are rates: the kernel exposes monotonic
// counters, so a Snapshot only exists as the delta between two samples. The
// Sampler keeps the latest computed Snapshot ready; consumers (heartbeat,
// OTel gauges) never compute at request time.
package hostinfo

import (
	"context"
	"regexp"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
)

const sampleInterval = 10 * time.Second

// Snapshot is one computed view of the node's resources.
type Snapshot struct {
	CPUPercent    float64 // 0–100, normalized across effective cores
	MemUsed       uint64
	MemTotal      uint64
	DiskUsed      uint64 // filesystem backing the configured path
	DiskTotal     uint64
	NetRxRate     uint64 // bytes/second
	NetTxRate     uint64
	DiskReadRate  uint64
	DiskWriteRate uint64
	Load1         float64
	SampledAt     time.Time
}

// Sampler periodically collects counters and derives rate snapshots.
type Sampler struct {
	diskPath string
	cg       cgroup

	mu     sync.RWMutex
	latest Snapshot
	ready  bool
	prev   counters
}

// New builds a sampler measuring disk usage of the filesystem containing
// diskPath (the node's data dir; "/" on the master for now).
func New(diskPath string) *Sampler {
	return &Sampler{diskPath: diskPath, cg: cgroup{root: cgroupRoot}}
}

// Latest returns the most recent snapshot; ok is false until two samples
// exist (rates need a delta).
func (s *Sampler) Latest() (Snapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.latest, s.ready
}

// Run samples until ctx is canceled. The first snapshot is ready one interval
// after start.
func (s *Sampler) Run(ctx context.Context) {
	ticker := time.NewTicker(sampleInterval)
	defer ticker.Stop()
	s.SampleNow(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.SampleNow(ctx)
		}
	}
}

// SampleNow takes one immediate reading (Run's ticker does this every
// interval; tests call it directly to warm a sampler synchronously).
func (s *Sampler) SampleNow(ctx context.Context) {
	cur := s.collect(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.prev.at.IsZero() {
		s.latest = derive(s.prev, cur)
		s.ready = true
	}
	s.prev = cur
}

// counters is one raw reading; Snapshot rates come from two of these.
type counters struct {
	at time.Time

	// CPU, one of two modes.
	cgroupCPU bool
	cpuUsec   uint64  // cgroup mode: cumulative usage_usec
	cpuBusy   float64 // host mode: cumulative busy seconds
	cpuTotal  float64 // host mode: cumulative total seconds
	cores     float64 // effective cores (cgroup quota or host count)

	// Gauges, copied through.
	memUsed, memTotal   uint64
	diskUsed, diskTotal uint64
	load1               float64

	// Cumulative byte counters.
	netRx, netTx        uint64
	diskRead, diskWrite uint64
}

func (s *Sampler) collect(ctx context.Context) counters {
	c := counters{at: time.Now()}

	// CPU: prefer the container's own quota when one is set.
	if cores, ok := s.cg.cpuLimit(); ok {
		if usec, ok := s.cg.cpuUsageUsec(); ok {
			c.cgroupCPU, c.cpuUsec, c.cores = true, usec, cores
		}
	}
	if !c.cgroupCPU {
		if times, err := cpu.TimesWithContext(ctx, false); err == nil && len(times) == 1 {
			t := times[0]
			total := t.User + t.System + t.Idle + t.Nice + t.Iowait + t.Irq + t.Softirq + t.Steal
			c.cpuBusy, c.cpuTotal = total-t.Idle-t.Iowait, total
		}
	}

	// Memory: cgroup limit wins; kubelet-style working set (usage minus
	// inactive page cache) so "used" doesn't count evictable cache.
	if limit, ok := s.cg.memLimit(); ok {
		c.memTotal = limit
		c.memUsed, _ = s.cg.memUsed()
	} else if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		c.memUsed, c.memTotal = vm.Used, vm.Total
	}

	// Disk usage: always the real filesystem behind the configured path.
	if du, err := disk.UsageWithContext(ctx, s.diskPath); err == nil {
		c.diskUsed, c.diskTotal = du.Used, du.Total
	}

	// Disk I/O: the container's cgroup when limited, else whole-disk devices.
	limited := c.cgroupCPU || func() bool { _, ok := s.cg.memLimit(); return ok }()
	if r, w, ok := s.cg.ioBytes(); limited && ok {
		c.diskRead, c.diskWrite = r, w
	} else if io, err := disk.IOCountersWithContext(ctx); err == nil {
		for name, d := range io {
			if partitionRe.MatchString(name) || virtualRe.MatchString(name) {
				continue
			}
			c.diskRead += d.ReadBytes
			c.diskWrite += d.WriteBytes
		}
	}

	// Network: per-namespace counters (honest inside containers); skip loopback.
	if nics, err := net.IOCountersWithContext(ctx, true); err == nil {
		for _, n := range nics {
			if n.Name == "lo" || n.Name == "lo0" {
				continue
			}
			c.netRx += n.BytesRecv
			c.netTx += n.BytesSent
		}
	}

	// Load average is not namespaced — inside a container it reflects the
	// whole host/VM. Reported anyway; consumers know it's advisory.
	if avg, err := load.AvgWithContext(ctx); err == nil {
		c.load1 = avg.Load1
	}

	return c
}

// derive computes a Snapshot from two consecutive readings. Pure — unit-tested
// directly.
func derive(prev, cur counters) Snapshot {
	dt := cur.at.Sub(prev.at).Seconds()
	if dt <= 0 {
		dt = 1
	}

	snap := Snapshot{
		MemUsed: cur.memUsed, MemTotal: cur.memTotal,
		DiskUsed: cur.diskUsed, DiskTotal: cur.diskTotal,
		Load1:     cur.load1,
		SampledAt: cur.at,
	}

	switch {
	case cur.cgroupCPU && prev.cgroupCPU && cur.cores > 0:
		used := float64(sub64(cur.cpuUsec, prev.cpuUsec)) / 1e6 // seconds of CPU time
		snap.CPUPercent = clampPct(used / dt / cur.cores * 100)
	case !cur.cgroupCPU && !prev.cgroupCPU && cur.cpuTotal > prev.cpuTotal:
		snap.CPUPercent = clampPct((cur.cpuBusy - prev.cpuBusy) / (cur.cpuTotal - prev.cpuTotal) * 100)
	}

	snap.NetRxRate = rate(prev.netRx, cur.netRx, dt)
	snap.NetTxRate = rate(prev.netTx, cur.netTx, dt)
	snap.DiskReadRate = rate(prev.diskRead, cur.diskRead, dt)
	snap.DiskWriteRate = rate(prev.diskWrite, cur.diskWrite, dt)
	return snap
}

// rate turns a cumulative-counter delta into bytes/second; counter resets
// (reboot, container restart) yield 0 instead of a bogus negative spike.
func rate(prev, cur uint64, dt float64) uint64 {
	if cur < prev {
		return 0
	}
	return uint64(float64(cur-prev) / dt)
}

func sub64(cur, prev uint64) uint64 {
	if cur < prev {
		return 0
	}
	return cur - prev
}

func clampPct(v float64) float64 {
	return min(100, max(0, v))
}

var (
	// Trailing-partition device names (sda1, nvme0n1p2, mmcblk0p1) are skipped
	// when summing I/O so disk+partition pairs don't double-count.
	partitionRe = regexp.MustCompile(`(?:[shvx][a-z]?d[a-z]+|nvme\d+n\d+p|mmcblk\d+p)\d+$`)
	// Pseudo block devices that never represent real I/O.
	virtualRe = regexp.MustCompile(`^(?:loop|ram|zram|fd)\d+$`)
)
