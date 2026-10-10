package workstats

import (
	"hash/fnv"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"k8s.io/client-go/util/workqueue"
)

// Queue is a client-go rate-limiting work queue that accounts for its
// traffic. Every arrival names a reason. A key's wait runs from the moment
// it first became runnable (an Add, or an AddAfter's delay running out)
// until a worker takes it, so an event that arrives while the key's
// previous pass still runs is charged the rest of that pass as well. Kind
// groups keys in the statistics: the substrate's claims, pools, buckets and
// object store; the kernel's queue has one kind.
type Queue[K comparable] struct {
	queue   workqueue.TypedRateLimitingInterface[K]
	limiter workqueue.TypedRateLimiter[K]
	kindOf  func(K) string

	mu sync.Mutex
	// runnable holds the first Add since a worker last took the key;
	// delayed the earliest pending AddAfter deadline, which is the one the
	// delaying queue keeps; taken the keys workers hold, and since when.
	runnable map[K]time.Time
	delayed  map[K]time.Time
	taken    map[K]time.Time
	kinds    map[string]*kindTotals
}

type kindTotals struct {
	arrivals map[string]uint64
	wait     Histogram
	pass     Histogram
}

// NewQueue builds the queue over limiter, the backoff AddRateLimited
// applies; kindOf names each key's kind.
func NewQueue[K comparable](limiter workqueue.TypedRateLimiter[K], kindOf func(K) string) *Queue[K] {
	return &Queue[K]{
		queue:    workqueue.NewTypedRateLimitingQueue(limiter),
		limiter:  limiter,
		kindOf:   kindOf,
		runnable: map[K]time.Time{},
		delayed:  map[K]time.Time{},
		taken:    map[K]time.Time{},
		kinds:    map[string]*kindTotals{},
	}
}

func (q *Queue[K]) totals(key K) *kindTotals {
	kind := q.kindOf(key)
	t, ok := q.kinds[kind]
	if !ok {
		t = &kindTotals{arrivals: map[string]uint64{}}
		q.kinds[kind] = t
	}
	return t
}

// Add makes key runnable now.
func (q *Queue[K]) Add(key K, reason string) {
	now := time.Now()
	q.mu.Lock()
	q.totals(key).arrivals[reason]++
	if _, ok := q.runnable[key]; !ok {
		q.runnable[key] = now
	}
	q.mu.Unlock()
	q.queue.Add(key)
}

// AddAfter makes key runnable once delay has passed.
func (q *Queue[K]) AddAfter(key K, delay time.Duration, reason string) {
	if delay <= 0 {
		q.Add(key, reason)
		return
	}
	at := time.Now().Add(delay)
	q.mu.Lock()
	q.totals(key).arrivals[reason]++
	if pending, ok := q.delayed[key]; !ok || at.Before(pending) {
		q.delayed[key] = at
	}
	q.mu.Unlock()
	q.queue.AddAfter(key, delay)
}

// Phase is id's fixed offset within interval, for periodic sweeps: a sweep
// that schedules every key AddAfter its phase spreads its passes across the
// interval and still reaches each key exactly one interval apart, which a
// fresh random offset per sweep would not (up to two intervals apart).
// Phase is zero for a non-positive interval.
func Phase(id uuid.UUID, interval time.Duration) time.Duration {
	if interval <= 0 {
		return 0
	}
	hash := fnv.New64a()
	_, _ = hash.Write(id[:])
	return time.Duration(hash.Sum64() % uint64(interval))
}

// AddRateLimited re-adds key after the limiter's backoff, exactly as the
// workqueue's own AddRateLimited does.
func (q *Queue[K]) AddRateLimited(key K, reason string) {
	q.AddAfter(key, q.limiter.When(key), reason)
}

// Get blocks until a key is runnable and hands it out, like the workqueue's
// Get; its wait is recorded.
func (q *Queue[K]) Get() (K, bool) {
	key, _, shutdown := q.Take()
	return key, shutdown
}

// Take is Get that also reports how long the key waited. The wait is zero
// and unrecorded when its start is unknown: a delayed add that fired just
// after a worker took the key for another reason.
func (q *Queue[K]) Take() (key K, waited time.Duration, shutdown bool) {
	key, shutdown = q.queue.Get()
	if shutdown {
		return key, 0, true
	}
	now := time.Now()
	q.mu.Lock()
	defer q.mu.Unlock()
	since, known := q.runnable[key]
	delete(q.runnable, key)
	if at, ok := q.delayed[key]; ok && !at.After(now) {
		if !known || at.Before(since) {
			since, known = at, true
		}
		delete(q.delayed, key)
	}
	q.taken[key] = now
	if known {
		waited = now.Sub(since)
		q.totals(key).wait.observe(waited)
	}
	return key, waited, false
}

// Done releases key like the workqueue's Done and records how long the
// worker held it.
func (q *Queue[K]) Done(key K) {
	q.mu.Lock()
	if at, ok := q.taken[key]; ok {
		q.totals(key).pass.observe(time.Since(at))
		delete(q.taken, key)
	}
	q.mu.Unlock()
	q.queue.Done(key)
}

// Forget clears key's backoff.
func (q *Queue[K]) Forget(key K) { q.queue.Forget(key) }

// Len counts the keys queued and runnable, excluding the ones workers hold
// and the ones still delayed.
func (q *Queue[K]) Len() int { return q.queue.Len() }

// ShutDown stops handing out keys.
func (q *Queue[K]) ShutDown() { q.queue.ShutDown() }

// QueueStats is a reading of a queue's accounting since the process
// started.
type QueueStats struct {
	Depth   int // keys queued and runnable
	Active  int // keys workers hold
	Workers int // filled in by the queue's owner
	Kinds   []KindStats
}

// KindStats is one kind's arrivals per reason, waits, and passes.
type KindStats struct {
	Kind     string
	Arrivals map[string]uint64
	Wait     Histogram
	Pass     Histogram
}

// Stats reads the queue's accounting, kinds in name order.
func (q *Queue[K]) Stats() QueueStats {
	depth := q.queue.Len()
	q.mu.Lock()
	defer q.mu.Unlock()
	stats := QueueStats{Depth: depth, Active: len(q.taken), Kinds: make([]KindStats, 0, len(q.kinds))}
	for kind, t := range q.kinds {
		arrivals := make(map[string]uint64, len(t.arrivals))
		for reason, n := range t.arrivals {
			arrivals[reason] = n
		}
		stats.Kinds = append(stats.Kinds, KindStats{Kind: kind, Arrivals: arrivals, Wait: t.wait.clone(), Pass: t.pass.clone()})
	}
	slices.SortFunc(stats.Kinds, func(a, b KindStats) int { return strings.Compare(a.Kind, b.Kind) })
	return stats
}

// Bounds are the upper bounds of a Histogram's buckets.
var Bounds = []time.Duration{
	100 * time.Millisecond, 250 * time.Millisecond, 500 * time.Millisecond,
	time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 20 * time.Second,
	30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute,
}

// Histogram is a cumulative distribution of durations over Bounds, in the
// Prometheus style: a sampler subtracts two readings to see an interval.
// Buckets[i] counts the observations at or below Bounds[i]; Count also
// includes the ones above the last bound.
type Histogram struct {
	Count   uint64
	Sum     time.Duration
	Max     time.Duration
	Buckets []uint64
}

func (h *Histogram) observe(d time.Duration) {
	if h.Buckets == nil {
		h.Buckets = make([]uint64, len(Bounds))
	}
	h.Count++
	h.Sum += d
	h.Max = max(h.Max, d)
	for i, bound := range Bounds {
		if d <= bound {
			h.Buckets[i]++
		}
	}
}

func (h Histogram) clone() Histogram {
	if h.Buckets == nil {
		h.Buckets = make([]uint64, len(Bounds))
	} else {
		h.Buckets = slices.Clone(h.Buckets)
	}
	return h
}
