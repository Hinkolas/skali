package hostinfo

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// cgroupRoot is where cgroup v2 exposes the calling process's own controllers
// (Docker enables cgroup namespaces, so inside a container these files
// describe the container). Overridden in tests via the cgroup struct.
const cgroupRoot = "/sys/fs/cgroup"

// cgroup reads the process's cgroup v2 interface files. Every method returns
// ok=false when the file is absent, malformed, or the controller reports "no
// limit" — callers then fall back to host-level readings.
type cgroup struct {
	root string
}

// cpuLimit parses cpu.max ("<quota> <period>" or "max <period>") into the
// number of effective cores the quota allows.
func (c cgroup) cpuLimit() (cores float64, ok bool) {
	fields := strings.Fields(c.read("cpu.max"))
	if len(fields) != 2 || fields[0] == "max" {
		return 0, false
	}
	quota, err1 := strconv.ParseFloat(fields[0], 64)
	period, err2 := strconv.ParseFloat(fields[1], 64)
	if err1 != nil || err2 != nil || quota <= 0 || period <= 0 {
		return 0, false
	}
	return quota / period, true
}

// cpuUsageUsec reads the cumulative usage_usec line from cpu.stat.
func (c cgroup) cpuUsageUsec() (uint64, bool) {
	return c.statLine("cpu.stat", "usage_usec")
}

// memLimit reads memory.max; "max" means unlimited (fall back to host).
func (c cgroup) memLimit() (uint64, bool) {
	s := strings.TrimSpace(c.read("memory.max"))
	if s == "" || s == "max" {
		return 0, false
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil || v == 0 {
		return 0, false
	}
	return v, true
}

// memUsed is the kubelet-style working set: memory.current minus inactive
// page cache (evictable), so a file-heavy workload doesn't read as "full".
func (c cgroup) memUsed() (uint64, bool) {
	s := strings.TrimSpace(c.read("memory.current"))
	current, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, false
	}
	if inactive, ok := c.statLine("memory.stat", "inactive_file"); ok && inactive < current {
		current -= inactive
	}
	return current, true
}

// ioBytes sums rbytes/wbytes across all devices in io.stat
// (lines: "MAJ:MIN rbytes=N wbytes=N rios=N ...").
func (c cgroup) ioBytes() (read, write uint64, ok bool) {
	raw := c.read("io.stat")
	if raw == "" {
		return 0, 0, false
	}
	for line := range strings.Lines(raw) {
		for field := range strings.FieldsSeq(line) {
			if v, found := strings.CutPrefix(field, "rbytes="); found {
				n, _ := strconv.ParseUint(v, 10, 64)
				read += n
			} else if v, found := strings.CutPrefix(field, "wbytes="); found {
				n, _ := strconv.ParseUint(v, 10, 64)
				write += n
			}
		}
	}
	return read, write, true
}

// statLine finds "key N" in a flat-keyed stat file.
func (c cgroup) statLine(file, key string) (uint64, bool) {
	for line := range strings.Lines(c.read(file)) {
		if rest, found := strings.CutPrefix(line, key+" "); found {
			v, err := strconv.ParseUint(strings.TrimSpace(rest), 10, 64)
			return v, err == nil
		}
	}
	return 0, false
}

func (c cgroup) read(name string) string {
	b, err := os.ReadFile(filepath.Join(c.root, name))
	if err != nil {
		return ""
	}
	return string(b)
}
