package engine

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
)

// inventoryInterval is deliberately slower than the container sampler's
// tick: images and volumes change on pulls and deploys, not continuously,
// and the report rides every heartbeat.
const inventoryInterval = 60 * time.Second

// InventorySampler keeps the node's image/volume inventory warm the same way
// Sampler does for containers: a background loop lists, consumers (heartbeat,
// the master's self-stamp) only ever read the cached result.
type InventorySampler struct {
	eng Engine

	mu     sync.RWMutex
	latest Inventory
	ready  bool
	down   bool // engine unreachable; logged on transitions only
}

func NewInventorySampler(eng Engine) *InventorySampler {
	return &InventorySampler{eng: eng}
}

// Latest returns the current inventory. ok is false while the engine state
// is unknown (no successful listing yet, or the engine went unreachable) —
// callers must treat that as "unknown", never as "nothing on the node". The
// returned slices are shared; callers must not mutate them.
func (s *InventorySampler) Latest() (Inventory, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.latest, s.ready
}

// Run samples until ctx is canceled.
func (s *InventorySampler) Run(ctx context.Context) {
	ticker := time.NewTicker(inventoryInterval)
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
func (s *InventorySampler) SampleNow(ctx context.Context) {
	inv, err := s.eng.Inventory(ctx)
	if err != nil {
		s.mu.Lock()
		// Unknown beats stale: while the engine is unreachable the previous
		// inventory must not keep being reported as live.
		s.ready = false
		wasDown := s.down
		s.down = true
		s.mu.Unlock()
		if !wasDown {
			slog.WarnContext(ctx, "container engine unreachable for inventory", "err", err)
		}
		return
	}

	// Largest first: the inventory exists to answer "what is eating disk".
	slices.SortFunc(inv.Images, func(a, b Image) int {
		if a.SizeBytes != b.SizeBytes {
			if a.SizeBytes > b.SizeBytes {
				return -1
			}
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	slices.SortFunc(inv.Volumes, func(a, b Volume) int { return strings.Compare(a.Name, b.Name) })

	s.mu.Lock()
	wasDown := s.down
	s.latest, s.ready, s.down = inv, true, false
	s.mu.Unlock()
	if wasDown {
		slog.InfoContext(ctx, "container engine reachable again for inventory")
	}
}
