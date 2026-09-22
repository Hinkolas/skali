package reconcile

import (
	"time"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/module"
)

// EnvironmentHealth is one environment's cached health rollup: the worst
// service health the last reconcile pass evaluated, and when.
//
// List and overview endpoints read this instead of running the status
// projection per environment; the projection (Status) stays the on-demand
// detail. A pass records a verdict exactly where it evaluates health after
// applying, so the value is the same Health the projection would compute
// from the same observed snapshot, at most one queue latency behind. A
// pass that exits before evaluation (a render failure, an apply error, a
// release command still running) leaves the previous verdict standing: the
// active revision keeps serving and the run journal explains the failure.
type EnvironmentHealth struct {
	Health      module.Health
	EvaluatedAt time.Time
}

// recordHealth stores the pass's post-apply verdict for one environment.
func (k *Kernel) recordHealth(environmentID uuid.UUID, statuses []ServiceStatus, now time.Time) {
	healths := make([]module.Health, len(statuses))
	for i := range statuses {
		healths[i] = statuses[i].Health
	}
	entry := EnvironmentHealth{Health: module.WorstHealth(healths), EvaluatedAt: now}
	k.healthMu.Lock()
	k.health[environmentID] = entry
	k.healthMu.Unlock()
}

// forgetHealth drops an environment that has nothing to evaluate: no target
// revision yet, taken down, releasing, or its row gone. Absence reads as
// unknown without an evaluation time, which is the truth.
func (k *Kernel) forgetHealth(environmentID uuid.UUID) {
	k.healthMu.Lock()
	delete(k.health, environmentID)
	k.healthMu.Unlock()
}

// sweepHealth drops verdicts for environments absent from the audit's
// authoritative set, the cleanup for rows removed without a pass (a raw
// environment delete). A verdict recorded after the set was read survives:
// its environment may have been created since.
func (k *Kernel) sweepHealth(known map[uuid.UUID]bool, listedAt time.Time) {
	k.healthMu.Lock()
	defer k.healthMu.Unlock()
	for id, entry := range k.health {
		if !known[id] && entry.EvaluatedAt.Before(listedAt) {
			delete(k.health, id)
		}
	}
}

// EnvironmentHealth reads one environment's cached verdict. ok is false
// when nothing was evaluated: before the first pass after boot, in API-only
// mode, for an environment that never deployed, or after teardown. While
// the cluster observation is not fresh the verdict folds to unknown, as
// module evaluation would on a stale view; EvaluatedAt keeps the time of
// the last real verdict.
func (k *Kernel) EnvironmentHealth(environmentID uuid.UUID) (EnvironmentHealth, bool) {
	fresh := k.observationFresh()
	k.healthMu.RLock()
	entry, ok := k.health[environmentID]
	k.healthMu.RUnlock()
	if !ok {
		return EnvironmentHealth{}, false
	}
	return foldHealth(entry, fresh), true
}

// EnvironmentHealths is the batch read for list endpoints: one lock
// acquisition and one observation read however many ids are asked for.
// Only environments with a verdict appear in the result.
func (k *Kernel) EnvironmentHealths(ids []uuid.UUID) map[uuid.UUID]EnvironmentHealth {
	fresh := k.observationFresh()
	result := make(map[uuid.UUID]EnvironmentHealth, len(ids))
	k.healthMu.RLock()
	defer k.healthMu.RUnlock()
	for _, id := range ids {
		if entry, ok := k.health[id]; ok {
			result[id] = foldHealth(entry, fresh)
		}
	}
	return result
}

// observationFresh reports whether the cluster observation is fresh. It is
// read before taking healthMu so the two locks never nest.
func (k *Kernel) observationFresh() bool {
	return k.deps.Observed != nil && k.deps.Observed.Source().State == module.SourceFresh
}

func foldHealth(entry EnvironmentHealth, fresh bool) EnvironmentHealth {
	if !fresh {
		entry.Health = module.HealthUnknown
	}
	return entry
}
