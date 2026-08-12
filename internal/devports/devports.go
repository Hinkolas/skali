// Package devports allocates the host ports skali dev intercepts route
// to. Allocation is deterministic: every (project, application, port
// name) hashes to a stable candidate inside the allocation range, so a
// project keeps its ports across sessions and the intercept rarely has
// to move. Only when the candidate is unavailable (another project's
// session, an unrelated process) does linear probing shift it.
package devports

import (
	"fmt"
	"hash/fnv"
	"net"
	"os"
	"sort"
	"strconv"

	"github.com/Hinkolas/skali/internal/localdev"
)

const (
	// DefaultBase and DefaultCount span the allocation range
	// [20000, 24999]: above the well-known dev-server ports people pin,
	// below the local platform's loopback service range.
	DefaultBase  = 20000
	DefaultCount = 5000
)

// Allocator hands out deterministic host ports for one dev session. It
// is not safe for concurrent use; a session allocates sequentially.
type Allocator struct {
	base, count int
	reserved    map[int]struct{}
	taken       map[int]struct{}
	probe       func(port int) bool
}

// New builds an allocator with an explicit range, reserved set, and
// bindability probe; tests inject a fake probe.
func New(base, count int, reserved []int, probe func(int) bool) *Allocator {
	reservedSet := make(map[int]struct{}, len(reserved))
	for _, port := range reserved {
		reservedSet[port] = struct{}{}
	}
	return &Allocator{
		base:     base,
		count:    count,
		reserved: reservedSet,
		taken:    make(map[int]struct{}),
		probe:    probe,
	}
}

// Default builds the production allocator: the SKALI_DEV_PORT_BASE
// override shifts the range (the localdev SKALI_DEV_* convention), the
// local platform's host ports are reserved, and bindability is a real
// bind test.
func Default() *Allocator {
	return New(envPortOr("SKALI_DEV_PORT_BASE", DefaultBase), DefaultCount,
		localdev.ReservedHostPorts(), portBindable)
}

// Allocate resolves one application's complete port set. names is the
// full required service-port name set; pins is the manifest dev.ports.
// Pins are used verbatim, and a pin that is not bindable is an error:
// drifting away from a declared port would silently detach the intercept
// from the process. Every unpinned name hashes into the range and probes
// upward.
func (a *Allocator) Allocate(project, app string, names []string, pins map[string]int) (map[string]int, error) {
	allocated := make(map[string]int, len(names))
	var auto []string
	for _, name := range names {
		pin, ok := pins[name]
		if !ok {
			auto = append(auto, name)
			continue
		}
		if !a.probe(pin) {
			return nil, fmt.Errorf("application %s: dev.ports pins %s to %d but that port is already in use on this host; free the port or change the pin",
				app, name, pin)
		}
		a.taken[pin] = struct{}{}
		allocated[name] = pin
	}
	// Sorted order keeps hash-collision probing deterministic across
	// sessions.
	sort.Strings(auto)
	for _, name := range auto {
		port, err := a.allocate(project, app, name)
		if err != nil {
			return nil, err
		}
		allocated[name] = port
	}
	return allocated, nil
}

func (a *Allocator) allocate(project, app, name string) (int, error) {
	digest := fnv.New32a()
	fmt.Fprintf(digest, "%s\x00%s\x00%s", project, app, name)
	candidate := a.base + int(digest.Sum32()%uint32(a.count))
	for attempt := 0; attempt < a.count; attempt++ {
		port := candidate + attempt
		if port >= a.base+a.count {
			port -= a.count
		}
		if _, ok := a.reserved[port]; ok {
			continue
		}
		if _, ok := a.taken[port]; ok {
			continue
		}
		if !a.probe(port) {
			continue
		}
		a.taken[port] = struct{}{}
		return port, nil
	}
	return 0, fmt.Errorf("no free dev port in range %d-%d for %s port %s; set SKALI_DEV_PORT_BASE or pin it in dev.ports",
		a.base, a.base+a.count-1, app, name)
}

// portBindable bind-tests on all interfaces on purpose: the dev server
// must accept non-loopback connections from the cluster gateway. The
// window between this test and the dev command binding is accepted; the
// session's post-start probe surfaces it.
func portBindable(port int) bool {
	listener, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}

// envPortOr mirrors the localdev convention for SKALI_DEV_* port
// overrides.
func envPortOr(name string, fallback int) int {
	if value := os.Getenv(name); value != "" {
		var port int
		if _, err := fmt.Sscanf(value, "%d", &port); err == nil && port > 0 {
			return port
		}
	}
	return fallback
}
