package reconcile

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/revision"
	"github.com/Hinkolas/skali/internal/store"
)

// rolloutDeadline is when the target's rollout runs out of time: the
// rollout budget from the rollout's start, and past every release Job of
// this promotion the Job's own timeout plus the budget again. A release
// Job enforces its timeout itself and the workloads roll only after it,
// so a release that starts late, behind a dependency that took its time,
// still gets its whole timeout and the rollout after it its whole budget.
// releaseStarts holds the services whose release the pass drove, with
// when their Job started; Jobs of this promotion the pass did not reach
// count from their creation.
func rolloutDeadline(rev *revision.Revision, target store.EnvironmentTarget, budget time.Duration,
	desired *desiredSet, snapshot observe.Snapshot, releaseStarts map[string]time.Time) time.Time {
	deadline := target.RolloutStartedAt.Add(budget)
	for service, objs := range desired.services {
		if objs.releaseJob == nil {
			continue
		}
		start, ok := releaseStarts[service]
		if !ok {
			live := liveReleaseJob(snapshot, objs.releaseJob.Namespace, objs.releaseJob.Name)
			if live == nil || live.Job == nil || !ofPromotion(live.Job, target) {
				continue
			}
			start = live.Job.Created
		}
		if end := start.Add(releaseTimeout(rev.Definition.Applications[service]) + budget); end.After(deadline) {
			deadline = end
		}
	}
	return deadline
}

// trafficSwitched reports whether the pass moved any application's
// traffic to a new color. The new color gets one confirmation pass before
// the deadline may move traffic back: falling back in the pass that
// switched would flip traffic twice on evidence from before the switch.
func trafficSwitched(plans map[string]trafficPlan) bool {
	for _, plan := range plans {
		if plan.switching {
			return true
		}
	}
	return false
}

// failRollout concludes a rollout that ran out of time, and reports
// whether the target fell back to the active revision. The run that
// explains the rollout fails with the health diagnostics. Without one (the
// run was cancelled or finished elsewhere) the deadline still holds: the
// fallback is journaled on a reconcile run of its own. A first deployment
// has nothing to fall back to and keeps its target, where level-triggered
// reconciliation continues and a late recovery still activates; without
// a run there is then nothing to record either.
func (k *Kernel) failRollout(ctx context.Context, attachment *runAttachment, target store.EnvironmentTarget,
	statuses []ServiceStatus, tls tlsOutcome, withheld map[string]string) (bool, error) {
	if !attachment.adopted() || !rolloutRun(attachment.run.Kind) {
		if target.ActiveRevisionID == nil {
			return false, nil
		}
		attachment.ensure(ctx)
	}
	attachment.completeStepFields(ctx, "", "verify", "Verify health", journal.StepFailed,
		[]string{strings.Join(healthSummary(statuses), "\n")}, healthFields(statuses))
	attachment.finish(ctx, journal.RunFailed, rolloutFailure(tls, statuses, withheld))
	// The guarded compare-and-swap makes a concurrent newer promotion win.
	rows, err := k.deps.Deploy.FallbackTargetLocked(ctx, store.FallbackEnvironmentTargetParams{
		EnvironmentID:    target.EnvironmentID,
		TargetRevisionID: target.TargetRevisionID,
	})
	if err != nil {
		return false, fmt.Errorf("reconcile: fall back target: %w", err)
	}
	if rows == 0 {
		return false, nil
	}
	slog.WarnContext(ctx, "rollout deadline exceeded; target returned to the active revision",
		"environment_id", target.EnvironmentID)
	return true, nil
}
