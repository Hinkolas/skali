// Package workstats accounts for where skalid's background work spends its
// time. Queue records why keys enter a work queue, how long they wait for a
// worker, and how long the worker holds them. Pass collects what one unit of
// work spends on database round trips, Kubernetes requests, the wait for
// the client's request budget, and the environment lock: the database
// tracer and the client-go hooks charge the pass found in the context of
// each statement or request. The system observation endpoint serves the
// totals and every pass logs its own breakdown, so a slow deployment
// handoff can be attributed without a metrics backend.
package workstats

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
)

// Unattributed is the caller of requests and statements made outside a
// pass: informers, API handlers, samplers.
const Unattributed = "other"

// A pass that ran for SlowPass, or waited LongWait for a worker, logs at
// info level; every other pass logs at debug level.
const (
	SlowPass = 5 * time.Second
	LongWait = 30 * time.Second
)

// Cost is what passes spent waiting on something other than their own
// computation. Kube includes the wait for the request budget, which
// Throttled reports again on its own.
type Cost struct {
	DBQueries    int
	DB           time.Duration
	DBAcquire    time.Duration
	KubeRequests int
	Kube         time.Duration
	Throttled    time.Duration
	LockConnect  time.Duration
	LockWait     time.Duration
}

func (c *Cost) add(other Cost) {
	c.DBQueries += other.DBQueries
	c.DB += other.DB
	c.DBAcquire += other.DBAcquire
	c.KubeRequests += other.KubeRequests
	c.Kube += other.Kube
	c.Throttled += other.Throttled
	c.LockConnect += other.LockConnect
	c.LockWait += other.LockWait
}

// Pass accumulates the cost of one unit of work. A worker attaches it to
// the context its pass runs under; every method is safe on a nil Pass, so
// code running outside a worker records nothing.
type Pass struct {
	caller  string
	started time.Time

	mu     sync.Mutex
	marked time.Time
	phases []phase
	cost   Cost
}

type phase struct {
	name string
	took time.Duration
}

type passKey struct{}

// NewPass starts a pass for caller ("kernel", "substrate", ...), the name
// its requests and totals are reported under.
func NewPass(caller string) *Pass {
	now := time.Now()
	return &Pass{caller: caller, started: now, marked: now}
}

// WithPass attaches the pass to ctx.
func WithPass(ctx context.Context, p *Pass) context.Context {
	return context.WithValue(ctx, passKey{}, p)
}

// PassFrom returns the pass attached to ctx, or nil.
func PassFrom(ctx context.Context) *Pass {
	p, _ := ctx.Value(passKey{}).(*Pass)
	return p
}

func callerOf(ctx context.Context) string {
	if p := PassFrom(ctx); p != nil {
		return p.caller
	}
	return Unattributed
}

// Mark closes the phase that ran since the previous mark, or since the
// start, under name.
func (p *Pass) Mark(name string) {
	if p == nil {
		return
	}
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.phases = append(p.phases, phase{name: name, took: now.Sub(p.marked)})
	p.marked = now
}

// AddLock charges the environment lock: connecting its dedicated session
// and waiting for the advisory lock.
func (p *Pass) AddLock(connect, wait time.Duration) {
	p.charge(func(c *Cost) {
		c.LockConnect += connect
		c.LockWait += wait
	})
}

func (p *Pass) charge(f func(*Cost)) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	f(&p.cost)
}

// Log ends the pass and logs it under msg with how long it waited for a
// worker, the caller's attrs, its phases (the time after the last mark is
// "rest"), and its cost.
func (p *Pass) Log(ctx context.Context, msg string, waited time.Duration, attrs ...any) {
	took, cost := p.end()
	level := slog.LevelDebug
	if took >= SlowPass || waited >= LongWait {
		level = slog.LevelInfo
	}
	slog.Log(ctx, level, msg, append(append([]any{"waited", round(waited)}, attrs...), cost...)...)
}

// end adds the pass to its caller's totals and returns how long it ran with
// the attributes describing its phases and cost.
func (p *Pass) end() (time.Duration, []any) {
	if p == nil {
		return 0, nil
	}
	now := time.Now()
	took := now.Sub(p.started)
	p.mu.Lock()
	cost := p.cost
	phases := make([]string, 0, len(p.phases)+1)
	for _, ph := range p.phases {
		phases = append(phases, ph.name+"="+round(ph.took).String())
	}
	if len(p.phases) > 0 {
		phases = append(phases, "rest="+round(now.Sub(p.marked)).String())
	}
	p.mu.Unlock()

	totals.mu.Lock()
	t, ok := totals.by[p.caller]
	if !ok {
		t = &CallerTotals{Caller: p.caller}
		totals.by[p.caller] = t
	}
	t.Passes++
	t.Took += took
	t.Cost.add(cost)
	totals.mu.Unlock()

	attrs := []any{"took", round(took)}
	if len(phases) > 0 {
		attrs = append(attrs, "phases", strings.Join(phases, " "))
	}
	return took, append(attrs,
		"db_queries", cost.DBQueries, "db", round(cost.DB), "db_acquire", round(cost.DBAcquire),
		"kube_requests", cost.KubeRequests, "kube", round(cost.Kube), "throttled", round(cost.Throttled),
		"lock_connect", round(cost.LockConnect), "lock_wait", round(cost.LockWait))
}

func round(d time.Duration) time.Duration { return d.Round(time.Millisecond) }

// CallerTotals sums the passes of one caller since the process started.
type CallerTotals struct {
	Caller string
	Passes uint64
	Took   time.Duration
	Cost
}

var totals = struct {
	mu sync.Mutex
	by map[string]*CallerTotals
}{by: map[string]*CallerTotals{}}

// Totals reads every caller's pass totals, ordered by caller.
func Totals() []CallerTotals {
	totals.mu.Lock()
	defer totals.mu.Unlock()
	out := make([]CallerTotals, 0, len(totals.by))
	for _, t := range totals.by {
		out = append(out, *t)
	}
	slices.SortFunc(out, func(a, b CallerTotals) int { return strings.Compare(a.Caller, b.Caller) })
	return out
}
