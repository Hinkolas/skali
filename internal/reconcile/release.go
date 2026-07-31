package reconcile

import (
	"context"
	"log/slog"
	"time"

	batchv1 "k8s.io/api/batch/v1"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/journal"
	rendering "github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/store"
)

// releaseState is one application's release-command position within the
// current rollout attempt.
type releaseState int

const (
	// releaseDone: the command completed; the workload may roll forward.
	releaseDone releaseState = iota
	// releaseRunning: the Job was created or still runs; the workload apply
	// waits and the pass requeues.
	releaseRunning
	// releaseFailed: the current attempt's Job failed terminally; the
	// rollout cannot proceed.
	releaseFailed
)

// releaseTailLines bounds the failure log tail journaled from the release
// Job's pod.
const releaseTailLines = int64(40)

// ensureRelease drives one application's release command toward completion:
// the per-revision Job is created when absent, watched while it runs, and
// its terminal state gates the workload apply. A failed Job created before
// the current promote (a leftover from an earlier attempt) is deleted so
// this attempt re-runs the command; a failed Job of the current attempt
// fails the rollout with the Job's log tail in the journal.
func (k *Kernel) ensureRelease(ctx context.Context, attachment *runAttachment, target store.EnvironmentTarget,
	service string, job *batchv1.Job, snapshot observe.Snapshot) (releaseState, string, error) {
	stepKey, title := "release:"+service, "Run release command for "+service
	live := liveReleaseJob(snapshot, job.Namespace, job.Name)
	if live == nil || live.Job == nil {
		changed, err := k.executeOps(ctx, []Op{{Kind: OpApply, Object: job}})
		if err != nil {
			k.journalOpFailure(ctx, attachment, stepKey, title, changed, err)
			return releaseRunning, "", err
		}
		if len(changed) > 0 {
			attachment.ensure(ctx)
		}
		reason := "the release command is starting"
		attachment.waitStep(ctx, stepKey, title, reason)
		return releaseRunning, reason, nil
	}
	status := live.Job
	switch {
	case status.Succeeded:
		attachment.completeStep(ctx, stepKey, title, journal.StepSucceeded,
			[]string{"release command completed"})
		return releaseDone, "", nil
	case status.Failed:
		if status.Created.Before(target.UpdatedAt) {
			// The failure predates the promote, so it belongs to an earlier
			// attempt; deleting it lets this attempt run afresh.
			if _, err := k.deps.Cluster.Delete(ctx, live.Ref); err != nil {
				k.journalOpFailure(ctx, attachment, stepKey, title, nil, err)
				return releaseRunning, "", err
			}
			reason := "retrying the release command"
			attachment.waitStep(ctx, stepKey, title, reason)
			return releaseRunning, reason, nil
		}
		// Only an in-flight run journals the failure (and pays for the log
		// read); the run-less resync passes that follow a failed first
		// deployment stay silent until a new promote retries.
		if attachment.active() {
			attachment.completeStep(ctx, stepKey, title, journal.StepFailed,
				k.releaseFailureLines(ctx, job.Namespace, job.Name, status))
		}
		return releaseFailed, "", nil
	default:
		reason := "the release command is running"
		attachment.waitStep(ctx, stepKey, title, reason)
		return releaseRunning, reason, nil
	}
}

// releaseFailureLines assembles the failure diagnostics: the Job's terminal
// reason plus a best-effort log tail from its pod.
func (k *Kernel) releaseFailureLines(ctx context.Context, namespace, name string, status *observe.JobStatus) []string {
	line := "the release command failed"
	if status.Reason == "DeadlineExceeded" {
		line = "the release command exceeded its timeout"
	}
	if status.Message != "" {
		line += ": " + status.Message
	}
	lines := []string{line}
	if k.deps.JobLogs == nil {
		return lines
	}
	tail, err := k.deps.JobLogs(ctx, namespace, name, releaseTailLines)
	if err != nil {
		slog.Warn("reconcile: read release logs", "job", name, "error", err)
		return lines
	}
	return append(lines, tail...)
}

func liveReleaseJob(snapshot observe.Snapshot, namespace, name string) *observe.Object {
	for index := range snapshot.Objects {
		obj := &snapshot.Objects[index]
		if obj.Kind == observe.KindReleaseJob && obj.Ref.Namespace == namespace && obj.Ref.Name == name {
			return obj
		}
	}
	return nil
}

// releaseBudget is the extra time the revision's release commands may
// legitimately take on top of the rollout deadline. Without it a long
// migration would trip the deadline while its Job still runs within its own
// timeout; the Job's activeDeadlineSeconds keeps the budget bounded.
func releaseBudget(definition compiler.ProjectDefinition) time.Duration {
	var budget time.Duration
	for _, application := range definition.Applications {
		command := application.Deployment.ReleaseCommand
		if len(command.Command) == 0 {
			continue
		}
		timeout := time.Duration(command.TimeoutMillis) * time.Millisecond
		if timeout <= 0 {
			timeout = rendering.DefaultReleaseTimeout
		}
		budget += timeout
	}
	return budget
}
