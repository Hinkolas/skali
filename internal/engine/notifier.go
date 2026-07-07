package engine

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"
)

const (
	// debounceWindow coalesces the burst a single operation emits
	// (create+start, untag+delete, …) into one resample and one ring. Fixed
	// window from the first event, not idle-reset, so latency stays bounded
	// under a steady event stream.
	debounceWindow = 400 * time.Millisecond

	eventsBackoffMin = time.Second
	eventsBackoffMax = 30 * time.Second
)

// Notifier turns the engine's raw event stream into fresh sampler state plus
// a doorbell: it debounces events, resamples the affected sampler
// synchronously, and only then rings subscribers — whoever hears the bell can
// read Latest() and see the state that caused it.
//
// The bell carries no payload and is lossy by design (buffered 1,
// non-blocking send): it means "something changed and the samplers are
// already fresh", nothing more. The samplers' periodic ticks remain the
// level-triggered safety net, so a lost event or dropped ring costs latency,
// never correctness.
type Notifier struct {
	eng        Engine
	containers *Sampler
	inventory  *InventorySampler

	// lost is owned by Run's goroutine: set when the event stream dies,
	// cleared by the first event of a replacement subscription.
	lost bool

	mu   sync.Mutex
	subs map[int]chan struct{}
	next int
}

func NewNotifier(eng Engine, containers *Sampler, inventory *InventorySampler) *Notifier {
	return &Notifier{eng: eng, containers: containers, inventory: inventory, subs: map[int]chan struct{}{}}
}

// Subscribe registers a doorbell channel; cancel removes it. The channel is
// never closed — consumers select against their own context.
func (n *Notifier) Subscribe() (bell <-chan struct{}, cancel func()) {
	ch := make(chan struct{}, 1)
	n.mu.Lock()
	id := n.next
	n.next++
	n.subs[id] = ch
	n.mu.Unlock()
	return ch, func() {
		n.mu.Lock()
		delete(n.subs, id)
		n.mu.Unlock()
	}
}

// Run consumes engine events until ctx is canceled, resubscribing with
// jittered backoff when the stream dies.
func (n *Notifier) Run(ctx context.Context) {
	backoff := eventsBackoffMin
	for {
		events, errs := n.eng.Events(ctx)
		if n.lost {
			// Anything may have happened while we were blind; refresh
			// everything so the bell's freshness promise holds again.
			n.refresh(ctx, true, true)
		}
		sawAny, err := n.consume(ctx, events, errs)
		if ctx.Err() != nil {
			return
		}
		if sawAny {
			backoff = eventsBackoffMin
		}
		if !n.lost {
			slog.WarnContext(ctx, "engine event stream lost", "err", err)
			n.lost = true
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff + rand.N(backoff/2)):
		}
		backoff = min(backoff*2, eventsBackoffMax)
	}
}

// consume pumps one subscription until it fails, debouncing bursts into a
// single resample + ring.
func (n *Notifier) consume(ctx context.Context, events <-chan EventKind, errs <-chan error) (sawAny bool, err error) {
	var (
		timer                 *time.Timer
		fire                  <-chan time.Time
		containers, inventory bool
	)
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return sawAny, ctx.Err()
		case err := <-errs:
			return sawAny, err
		case kind := <-events:
			sawAny = true
			if n.lost {
				n.lost = false
				slog.InfoContext(ctx, "engine event stream restored")
			}
			switch kind {
			case EventContainer:
				containers = true
			case EventImage, EventVolume:
				inventory = true
			}
			if timer == nil {
				timer = time.NewTimer(debounceWindow)
				fire = timer.C
			}
		case <-fire:
			n.refresh(ctx, containers, inventory)
			containers, inventory = false, false
			timer, fire = nil, nil
		}
	}
}

// refresh resamples the stale surfaces, then rings. Sampling happens before
// the ring so a poked heartbeat reads post-change state, never pre-change.
func (n *Notifier) refresh(ctx context.Context, containers, inventory bool) {
	if containers && n.containers != nil {
		n.containers.SampleNow(ctx)
	}
	if inventory && n.inventory != nil {
		n.inventory.SampleNow(ctx)
	}
	if !containers && !inventory {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, ch := range n.subs {
		select {
		case ch <- struct{}{}:
		default: // a pending ring already says everything this one would
		}
	}
}
