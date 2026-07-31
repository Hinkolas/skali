package observe

import (
	"log/slog"
	"time"

	"github.com/Hinkolas/skali/internal/lifecycle"
	"github.com/Hinkolas/skali/internal/module"
)

// kindNamespace is a store-internal kind: observed for indexing and audit
// but never handed to module evaluation.
const kindNamespace = "namespace"

// SourceKubernetes is the cluster watch source, registered by every store.
// Provider observers (REWORK_V2 7.4) register additional named sources; all
// sources share one fresh/stale/unknown model but fail independently, so one
// provider's outage never poisons another source's projections.
const SourceKubernetes = "kubernetes"

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

// sourceRecord is one source's freshness accounting.
type sourceRecord struct {
	state      string // module.SourceUnknown | SourceFresh | SourceStale
	ready      bool
	lastSync   time.Time
	failedAt   time.Time
	staleSince time.Time
}

// transition moves the record's freshness state under the machine's guard;
// an invalid transition is a bug caught by tests, never a silent write.
func (r *sourceRecord) transition(to string) bool {
	if r.state == to {
		return false
	}
	if !Freshness.Can(r.state, to) {
		return false
	}
	r.state = to
	return true
}

// RegisterSource makes a named source known to the store in state unknown.
// Idempotent; a source's own Run loop registers itself before marking
// anything.
func (s *Store) RegisterSource(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sources[name]; !ok {
		s.sources[name] = &sourceRecord{state: module.SourceUnknown}
	}
}

// sourceLocked resolves a named source, registering it defensively so a
// mark on an unregistered name is never a nil dereference.
func (s *Store) sourceLocked(name string) *sourceRecord {
	record, ok := s.sources[name]
	if !ok {
		record = &sourceRecord{state: module.SourceUnknown}
		s.sources[name] = record
	}
	return record
}

// SourceReady reports whether one named source completed its initial sync.
func (s *Store) SourceReady(source string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.sources[source]
	return ok && record.ready
}

// MarkReady flips one source to fresh after its initial sync completed.
func (s *Store) MarkReady(source string) {
	now := s.clock()
	s.mu.Lock()
	record := s.sourceLocked(source)
	record.ready = true
	changed := record.transition(module.SourceFresh)
	record.lastSync = now
	record.staleSince = time.Time{}
	s.mu.Unlock()
	if changed {
		slog.Info("observe: source fresh", "source", source)
		s.invalidateAll()
	}
}

// MarkUnready returns one source to unknown, used on shutdown or before a
// cache rebuild so no stale view masquerades as fresh.
func (s *Store) MarkUnready(source string) {
	s.mu.Lock()
	record := s.sourceLocked(source)
	record.ready = false
	changed := record.transition(module.SourceUnknown)
	s.mu.Unlock()
	if changed {
		slog.Info("observe: source unknown", "source", source)
		s.invalidateAll()
	}
}

// MarkContact records one source's successful list, watch establishment, or
// probe; it recovers a stale source to fresh.
func (s *Store) MarkContact(source string) {
	now := s.clock()
	s.mu.Lock()
	record := s.sourceLocked(source)
	record.lastSync = now
	record.failedAt = time.Time{}
	changed := false
	var staleFor time.Duration
	if record.state == module.SourceStale {
		changed = record.transition(module.SourceFresh)
		if !record.staleSince.IsZero() {
			staleFor = now.Sub(record.staleSince)
		}
		record.staleSince = time.Time{}
	}
	s.mu.Unlock()
	if changed {
		slog.Info("observe: source recovered", "source", source, "stale_for", staleFor)
		s.invalidateAll()
	}
}

// MarkFailure records one source's contact failure. The source turns stale
// only when no successful contact follows within the threshold;
// EvaluateFreshness applies that decision.
func (s *Store) MarkFailure(source string) {
	now := s.clock()
	s.mu.Lock()
	record := s.sourceLocked(source)
	if record.failedAt.IsZero() {
		record.failedAt = now
	}
	s.mu.Unlock()
}

// EvaluateFreshness applies one source's staleness decision: a recorded
// failure with no successful contact after it, older than the threshold,
// turns a fresh source stale. Called periodically by each source at its own
// cadence.
func (s *Store) EvaluateFreshness(source string, threshold time.Duration) {
	now := s.clock()
	s.mu.Lock()
	record := s.sourceLocked(source)
	changed := false
	if record.state == module.SourceFresh && !record.failedAt.IsZero() && now.Sub(record.failedAt) >= threshold {
		changed = record.transition(module.SourceStale)
		if changed {
			record.staleSince = record.failedAt
		}
	}
	failedAt := record.failedAt
	s.mu.Unlock()
	if changed {
		// A stale source turns every module health unknown: applies of
		// dependent services and activations block until recovery.
		slog.Warn("observe: source stale", "source", source, "failed_at", failedAt)
		s.invalidateAll()
	}
}

// invalidateAll nudges every environment's subscribers after a source
// state change: their health projections just changed meaning.
func (s *Store) invalidateAll() {
	for _, environment := range s.environments() {
		s.invalidate(environment)
	}
}
