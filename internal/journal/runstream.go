package journal

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

// runstream.go is the invalidation plane for run trees and per-environment
// run lists: payload-free signals (read the journal, same contract as the
// observe invalidation stream). A subscriber that falls behind is
// disconnected (channel closed) and resubscribes, re-reading the state.

// signalBroadcaster fans payload-free change signals out per key.
type signalBroadcaster struct {
	mu   sync.Mutex
	subs map[uuid.UUID]map[chan struct{}]struct{}
}

func newSignalBroadcaster() *signalBroadcaster {
	return &signalBroadcaster{subs: make(map[uuid.UUID]map[chan struct{}]struct{})}
}

func (b *signalBroadcaster) subscribe(key uuid.UUID) (chan struct{}, func()) {
	ch := make(chan struct{}, 16)
	b.mu.Lock()
	if b.subs[key] == nil {
		b.subs[key] = make(map[chan struct{}]struct{})
	}
	b.subs[key][ch] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			if set, ok := b.subs[key]; ok {
				if _, subscribed := set[ch]; subscribed {
					delete(set, ch)
					close(ch)
				}
				if len(set) == 0 {
					delete(b.subs, key)
				}
			}
			b.mu.Unlock()
		})
	}
	return ch, cancel
}

func (b *signalBroadcaster) publish(key uuid.UUID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	set := b.subs[key]
	for ch := range set {
		select {
		case ch <- struct{}{}:
		default:
			delete(set, ch)
			close(ch)
		}
	}
	if set != nil && len(set) == 0 {
		delete(b.subs, key)
	}
}

// active reports whether any subscriber exists at all: the fast path that
// keeps watcher-only row reads off the no-watcher hot path.
func (b *signalBroadcaster) active() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs) > 0
}

// SubscribeRunTree signals whenever one run's tree (run status, steps,
// attempts, progress) may have changed. Signals carry no payload; re-read
// the tree.
func (s *Service) SubscribeRunTree(runID uuid.UUID) (<-chan struct{}, func()) {
	return s.runWatch.subscribe(runID)
}

// SubscribeEnvironmentRuns signals whenever one environment's run list may
// have changed (a run created, started, finished, or pruned).
func (s *Service) SubscribeEnvironmentRuns(environmentID uuid.UUID) (<-chan struct{}, func()) {
	return s.envRunsWatch.subscribe(environmentID)
}

// notifyRun publishes a run-tree change. Call only after the transaction
// committed, so subscribers never re-read rolled-back state.
func (s *Service) notifyRun(runID uuid.UUID) {
	s.runWatch.publish(runID)
}

// notifyEnvironment publishes a run-list change; nil-safe for runs outside
// an environment.
func (s *Service) notifyEnvironment(environmentID *uuid.UUID) {
	if environmentID == nil {
		return
	}
	s.envRunsWatch.publish(*environmentID)
}

// notifyRunOfStep resolves a step to its run and publishes, but only while
// someone is actually watching a run tree: the resolving read is the cost
// this guard avoids on the hot path.
func (s *Service) notifyRunOfStep(ctx context.Context, stepID uuid.UUID) {
	if !s.runWatch.active() {
		return
	}
	step, err := s.st.GetStepByID(ctx, stepID)
	if err != nil {
		return
	}
	s.notifyRun(step.RunID)
}
