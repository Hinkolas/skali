package observe

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Probe returns one provider's complete current object set. A poll model
// has no incremental events: each successful probe is the whole truth, and
// ReplaceSource reconciles the store to it.
type Probe func(ctx context.Context) ([]Object, error)

// PollOptions configures one poll-based provider source (REWORK_V2 7.4:
// systems without a useful watch API update the same ObservedStore contract
// with explicit freshness at their own cadence).
type PollOptions struct {
	// Source is the registered source name, e.g. "seaweedfs".
	Source string
	// Interval between probes; also the freshness evaluation cadence.
	Interval time.Duration
	// StaleThreshold turns the source stale after a failure with no
	// successful probe inside the window.
	StaleThreshold time.Duration
	// Timeout bounds one probe.
	Timeout time.Duration
	// Enqueue receives every environment whose projections a probe changed.
	Enqueue func(environmentID uuid.UUID)
}

const (
	defaultPollInterval = 15 * time.Second
	defaultPollStale    = 45 * time.Second
	defaultProbeTimeout = 10 * time.Second
)

// PollSource runs one provider probe on a ticker, owning the freshness
// discipline so probes stay pure data producers. Poke requests an immediate
// re-poll after a mutation (coalesced), keeping usage and existence honest
// without waiting a full interval.
type PollSource struct {
	store *Store
	probe Probe
	opts  PollOptions
	poke  chan struct{}
}

func NewPollSource(store *Store, probe Probe, opts PollOptions) *PollSource {
	if opts.Interval <= 0 {
		opts.Interval = defaultPollInterval
	}
	if opts.StaleThreshold <= 0 {
		opts.StaleThreshold = defaultPollStale
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaultProbeTimeout
	}
	return &PollSource{store: store, probe: probe, opts: opts, poke: make(chan struct{}, 1)}
}

// Poke requests an immediate re-poll; concurrent pokes coalesce into one.
func (p *PollSource) Poke() {
	select {
	case p.poke <- struct{}{}:
	default:
	}
}

// Run polls until the context ends. The source registers itself, marks
// ready on its first successful probe (a complete probe IS the initial
// sync), and returns to unknown on shutdown so no stale view masquerades as
// fresh.
func (p *PollSource) Run(ctx context.Context) error {
	p.store.RegisterSource(p.opts.Source)
	defer p.store.MarkUnready(p.opts.Source)

	ticker := time.NewTicker(p.opts.Interval)
	defer ticker.Stop()

	p.pollOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		case <-p.poke:
		}
		p.pollOnce(ctx)
	}
}

// pollOnce runs one probe cycle: success replaces the source's object set
// and recovers freshness, failure records itself and lets the threshold
// decide staleness.
func (p *PollSource) pollOnce(ctx context.Context) {
	probeCtx, cancel := context.WithTimeout(ctx, p.opts.Timeout)
	objects, err := p.probe(probeCtx)
	cancel()
	if err != nil {
		p.store.MarkFailure(p.opts.Source)
		p.store.EvaluateFreshness(p.opts.Source, p.opts.StaleThreshold)
		return
	}
	if !p.store.SourceReady(p.opts.Source) {
		p.store.MarkReady(p.opts.Source)
	} else {
		p.store.MarkContact(p.opts.Source)
	}
	affected := p.store.ReplaceSource(p.opts.Source, objects)
	if p.opts.Enqueue != nil {
		for _, environment := range affected {
			p.opts.Enqueue(environment)
		}
	}
	p.store.EvaluateFreshness(p.opts.Source, p.opts.StaleThreshold)
}
