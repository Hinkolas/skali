package journal

import (
	"context"

	"github.com/google/uuid"
)

// runstream.go is the invalidation plane for run trees and per-environment
// run lists: payload-free signals (read the journal, same contract as the
// observe invalidation stream). A subscriber that falls behind is
// disconnected (channel closed) and resubscribes, re-reading the state.

// SubscribeRunTree signals whenever one run's tree (run status, steps,
// attempts, progress) may have changed. Signals carry no payload; re-read
// the tree.
func (s *Service) SubscribeRunTree(runID uuid.UUID) (<-chan struct{}, func()) {
	return s.runWatch.Subscribe(runID)
}

// SubscribeEnvironmentRuns signals whenever one environment's run list may
// have changed (a run created, started, finished, or pruned).
func (s *Service) SubscribeEnvironmentRuns(environmentID uuid.UUID) (<-chan struct{}, func()) {
	return s.envRunsWatch.Subscribe(environmentID)
}

// notifyRun publishes a run-tree change. Call only after the transaction
// committed, so subscribers never re-read rolled-back state.
func (s *Service) notifyRun(runID uuid.UUID) {
	s.runWatch.Publish(runID, struct{}{})
}

// notifyEnvironment publishes a run-list change; nil-safe for runs outside
// an environment.
func (s *Service) notifyEnvironment(environmentID *uuid.UUID) {
	if environmentID == nil {
		return
	}
	s.envRunsWatch.Publish(*environmentID, struct{}{})
}

// notifyRunOfStep resolves a step to its run and publishes, but only while
// someone is actually watching a run tree: the resolving read is the cost
// this guard avoids on the hot path.
func (s *Service) notifyRunOfStep(ctx context.Context, stepID uuid.UUID) {
	if !s.runWatch.Active() {
		return
	}
	step, err := s.st.GetStepByID(ctx, stepID)
	if err != nil {
		return
	}
	s.notifyRun(step.RunID)
}
