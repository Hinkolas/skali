package engine

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	sampleInterval = 10 * time.Second
	// statsConcurrency bounds the per-tick stats fan-out: one daemon
	// roundtrip per running container.
	statsConcurrency = 4
)

// Observation is one container plus its latest derived stats. Stats is nil
// until the sampler has two readings for that container (rates need a delta).
type Observation struct {
	Container
	Stats *Stats
}

// Sampler keeps the node's observed container state warm, the same way
// hostinfo.Sampler does for host metrics: a background loop lists and
// samples, consumers (heartbeat, the master's self-stamp) only ever read the
// cached result. Nothing in a request path talks to the daemon.
type Sampler struct {
	eng Engine

	mu     sync.RWMutex
	latest []Observation
	ready  bool
	prev   map[string]RawStats // cumulative counters by container id
	down   bool                // engine unreachable; logged on transitions only
}

func NewSampler(eng Engine) *Sampler {
	return &Sampler{eng: eng, prev: map[string]RawStats{}}
}

// Latest returns the current observations. ok is false while the engine
// state is unknown (no successful listing yet, or the engine went
// unreachable) — callers must treat that as "unknown", never as "no
// containers". The returned slice is shared; callers must not mutate it.
func (s *Sampler) Latest() ([]Observation, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.latest, s.ready
}

// Run samples until ctx is canceled.
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
	listed, err := s.eng.List(ctx)
	if err != nil {
		s.mu.Lock()
		// Unknown beats stale: while the engine is unreachable the previous
		// observations must not keep being reported as live.
		s.ready = false
		wasDown := s.down
		s.down = true
		s.mu.Unlock()
		if !wasDown {
			slog.WarnContext(ctx, "container engine unreachable", "err", err)
		}
		return
	}

	s.mu.RLock()
	prev := s.prev
	wasDown := s.down
	s.mu.RUnlock()
	if wasDown {
		slog.InfoContext(ctx, "container engine reachable again")
	}

	obs := make([]Observation, len(listed))
	raws := make([]*RawStats, len(listed))
	sem := make(chan struct{}, statsConcurrency)
	var wg sync.WaitGroup
	for i, c := range listed {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			obs[i], raws[i] = s.observe(ctx, c, prev)
		}()
	}
	wg.Wait()

	next := make(map[string]RawStats, len(listed))
	kept := obs[:0]
	for i, o := range obs {
		if o.ID == "" { // container raced away between List and Inspect
			continue
		}
		kept = append(kept, o)
		if raws[i] != nil {
			next[o.ID] = *raws[i]
		}
	}
	slices.SortFunc(kept, func(a, b Observation) int { return strings.Compare(a.Name, b.Name) })

	s.mu.Lock()
	s.latest, s.ready, s.prev, s.down = kept, true, next, false
	s.mu.Unlock()
}

// observe resolves one listed container to full detail plus, when running,
// a stats reading and the derived snapshot.
func (s *Sampler) observe(ctx context.Context, c Container, prev map[string]RawStats) (Observation, *RawStats) {
	full, err := s.eng.Inspect(ctx, c.ID)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			slog.DebugContext(ctx, "inspect container", "id", c.ID, "err", err)
		}
		return Observation{}, nil
	}
	o := Observation{Container: full}
	if full.State != "running" {
		return o, nil
	}
	raw, err := s.eng.Stats(ctx, full.ID)
	if err != nil {
		slog.DebugContext(ctx, "container stats", "id", c.ID, "err", err)
		return o, nil
	}
	if p, ok := prev[full.ID]; ok {
		st := deriveStats(p, raw)
		o.Stats = &st
	}
	return o, &raw
}

// deriveStats computes a snapshot from two consecutive readings. Pure —
// unit-tested directly.
func deriveStats(prev, cur RawStats) Stats {
	dt := cur.At.Sub(prev.At).Seconds()
	if dt <= 0 {
		dt = 1
	}
	st := Stats{MemUsed: cur.MemUsed, MemLimit: cur.MemLimit}
	// docker-stats semantics: 100 = one full core. Clamped to the visible
	// core count so counter glitches can't report impossible values.
	pct := float64(sub64(cur.CPUTotalNs, prev.CPUTotalNs)) / 1e9 / dt * 100
	if cur.OnlineCPUs > 0 {
		pct = min(pct, float64(cur.OnlineCPUs)*100)
	}
	st.CPUPercent = max(0, pct)
	st.NetRxRate = rate(prev.NetRx, cur.NetRx, dt)
	st.NetTxRate = rate(prev.NetTx, cur.NetTx, dt)
	return st
}

// rate turns a cumulative-counter delta into bytes/second; counter resets
// (container restart) yield 0 instead of a bogus negative spike.
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
