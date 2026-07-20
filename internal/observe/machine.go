package observe

import (
	"time"

	"github.com/Hinkolas/skali/internal/lifecycle"
	"github.com/Hinkolas/skali/internal/module"
)

// kindNamespace is a store-internal kind: observed for indexing and audit
// but never handed to module evaluation.
const kindNamespace = "namespace"

// Freshness is the observation source lifecycle. Unknown is initial (no
// cluster, or the cache is still rebuilding); fresh follows a completed
// sync; stale marks a watch failure without timely re-establishment.
// Recovery goes stale to fresh; a shutdown or cache rebuild returns to
// unknown from either live state.
var Freshness = lifecycle.Machine[string]{
	States: []string{module.SourceUnknown, module.SourceFresh, module.SourceStale},
	Transitions: map[string][]string{
		module.SourceUnknown: {module.SourceFresh},
		module.SourceFresh:   {module.SourceStale, module.SourceUnknown},
		module.SourceStale:   {module.SourceFresh, module.SourceUnknown},
	},
}

// MarkReady flips the store to fresh after the initial cache sync.
func (s *Store) MarkReady() {
	now := s.clock()
	s.mu.Lock()
	s.ready = true
	changed := s.transitionLocked(module.SourceFresh)
	s.lastSync = now
	s.staleSince = time.Time{}
	s.mu.Unlock()
	if changed {
		s.invalidateAll()
	}
}

// MarkUnready returns the store to unknown, used on shutdown or before a
// cache rebuild so no stale view masquerades as fresh.
func (s *Store) MarkUnready() {
	s.mu.Lock()
	s.ready = false
	changed := s.transitionLocked(module.SourceUnknown)
	s.mu.Unlock()
	if changed {
		s.invalidateAll()
	}
}

// MarkContact records a successful list or watch establishment; it recovers
// a stale source to fresh.
func (s *Store) MarkContact() {
	now := s.clock()
	s.mu.Lock()
	s.lastSync = now
	s.failedAt = time.Time{}
	changed := false
	if s.state == module.SourceStale {
		changed = s.transitionLocked(module.SourceFresh)
		s.staleSince = time.Time{}
	}
	s.mu.Unlock()
	if changed {
		s.invalidateAll()
	}
}

// MarkFailure records a list/watch failure. The source turns stale only
// when no successful contact follows within the threshold; EvaluateFreshness
// applies that decision.
func (s *Store) MarkFailure() {
	now := s.clock()
	s.mu.Lock()
	if s.failedAt.IsZero() {
		s.failedAt = now
	}
	s.mu.Unlock()
}

// EvaluateFreshness applies the staleness decision: a recorded failure with
// no successful contact after it, older than the threshold, turns a fresh
// source stale. Called periodically by the watch source.
func (s *Store) EvaluateFreshness(threshold time.Duration) {
	now := s.clock()
	s.mu.Lock()
	changed := false
	if s.state == module.SourceFresh && !s.failedAt.IsZero() && now.Sub(s.failedAt) >= threshold {
		changed = s.transitionLocked(module.SourceStale)
		if changed {
			s.staleSince = s.failedAt
		}
	}
	s.mu.Unlock()
	if changed {
		s.invalidateAll()
	}
}

// transitionLocked moves the freshness state under the machine's guard;
// an invalid transition is a bug caught by tests, never a silent write.
func (s *Store) transitionLocked(to string) bool {
	if s.state == to {
		return false
	}
	if !Freshness.Can(s.state, to) {
		return false
	}
	s.state = to
	return true
}

// invalidateAll nudges every environment's subscribers after a source
// state change: their health projections just changed meaning.
func (s *Store) invalidateAll() {
	for _, environment := range s.environments() {
		s.invalidate(environment)
	}
}
