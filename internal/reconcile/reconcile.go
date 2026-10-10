package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/deploy"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/kube"
	rendering "github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/redact"
	"github.com/Hinkolas/skali/internal/revision"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/workstats"
)

// requeueHealthCheck is the safety interval while waiting on health; watch
// events normally beat it.
const requeueHealthCheck = 15 * time.Second

// reconcileEnvironment is one level-triggered pass: load the target
// revision, render the desired state, apply and prune idempotently,
// evaluate health, and activate when the revision's health conditions pass.
// A pass that changes nothing writes no journal rows.
func (k *Kernel) reconcileEnvironment(ctx context.Context, environmentID uuid.UUID) (time.Duration, error) {
	// Edge probes are network waits (5s each); they run before the lock so
	// a pass holds it only for database and cluster work and a Promote or
	// Rollback waiting on the same lock is never parked behind DNS.
	// Phase marks split the pass's duration in its log line (workstats).
	pass := workstats.PassFrom(ctx)
	inputs := &passInputs{kernel: k, environmentID: environmentID}
	probeInterval := k.prepareEdgeVerdicts(ctx, environmentID, inputs)
	pass.Mark("probe")

	unlock, lockErr := k.deps.Store.LockEnvironment(ctx, environmentID)
	if lockErr != nil {
		return 0, lockErr
	}
	defer unlock()
	pass.Mark("lock")

	state, err := k.deps.Store.GetEnvironmentPass(ctx, environmentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			k.forgetHealth(environmentID)
			return 0, nil // environment deleted; the audit reports orphans
		}
		return 0, fmt.Errorf("reconcile: get environment: %w", err)
	}
	env, target := state.Environment, state.EnvironmentTarget
	if target.State != deploy.EnvironmentStateActive {
		// A persisted destructive decision replaces convergence entirely:
		// desired state is absence, so no health verdict stands either.
		k.forgetHealth(environmentID)
		return k.teardownEnvironment(ctx, state)
	}
	if target.TargetRevisionID == nil {
		k.forgetHealth(environmentID) // created, never deployed
		return 0, nil
	}
	rev, err := inputs.revision(ctx, *target.TargetRevisionID)
	if err != nil {
		return 0, fmt.Errorf("reconcile: load target revision: %w", err)
	}

	attachment := k.attachRun(ctx, state, func(ctx context.Context) *redact.Redactor {
		return k.redactor(ctx, environmentID, rev, inputs)
	})

	intercepts, err := k.loadIntercepts(ctx, environmentID)
	if err != nil {
		return 0, err
	}
	appRestarts, err := k.loadAppRestarts(ctx, environmentID)
	if err != nil {
		return 0, err
	}
	// Output generations read before rendering describe the claims as
	// provisioned so far; a claim that provisions during this pass reaches
	// its consumers' templates on the next one, which is also the first
	// pass that lets them apply.
	outputs, err := k.claimOutputs(ctx, environmentID)
	if err != nil {
		return 0, err
	}
	pass.Mark("load")

	// The traffic decision reads the live Service selectors and workload
	// availability before rendering: blue-green applications keep their
	// serving color until the new one is fully available.
	desired, err := k.desiredSet(ctx, inputs, rev, target.RestartedAt, appRestarts, outputs.Generations, outputs.BucketNames, intercepts, env.Priority,
		k.deps.Observed.Snapshot(environmentID))
	pass.Mark("render")
	if err != nil {
		// Whatever stops this target from rendering, the pods of the active
		// revision keep running: they stay isolated regardless.
		if target.ActiveRevisionID != nil {
			if applyErr := k.applyIsolation(ctx, environmentID, rev); applyErr != nil {
				return 0, applyErr
			}
		}
		var gateway *hostGatewayUnavailable
		if errors.As(err, &gateway) {
			return k.waitHostGateway(ctx, attachment, environmentID, target, rev, gateway.cause)
		}
		// An unrenderable revision is permanent for this target: journal the
		// diagnostic, never prune (compiler-error absence must not delete
		// anything), and wait for a new target instead of spinning. The
		// kernel's resync ticker re-picks the environment while target and
		// active disagree, so a later fix lands within one interval.
		slog.Warn("reconcile: desired state failed", "environment", environmentID, "error", err)
		attachment.completeStep(ctx, "render", "Render desired state", journal.StepFailed,
			[]string{"rendering the desired state failed: " + err.Error()})
		if attachment.adopted() {
			attachment.finish(ctx, journal.RunFailed, "rendering the desired state failed: "+err.Error())
		}
		return 0, nil
	}
	// A pass that waited on the host gateway closes that wait now that the
	// desired state rendered.
	attachment.resolveWait(ctx, "render", "Render desired state",
		[]string{"the host gateway resolved; desired state rendered"})
	batches, waiting, err := planBatches(rev.Definition)
	if err != nil {
		slog.Warn("reconcile: ordering failed", "environment", environmentID, "error", err)
		attachment.completeStep(ctx, "render", "Render desired state", journal.StepFailed,
			[]string{"ordering services failed: " + err.Error()})
		if attachment.adopted() {
			attachment.finish(ctx, journal.RunFailed, "ordering services failed: "+err.Error())
		}
		return 0, nil
	}

	// Desired claims are recorded before health is read, so the projections
	// the evaluation sees are at least as fresh as this pass's intent. The
	// substrate provisions asynchronously; states carry readiness and the
	// visible waiting reasons.
	claimWaiting, err := k.ensureClaims(ctx, env.ProjectID, environmentID, rev, desired.bucketRoutes)
	if err != nil {
		k.journalOpFailure(ctx, attachment, "claims", "Record database claims", nil, err)
		return 0, err
	}
	pass.Mark("claims")

	snapshot := k.deps.Observed.Snapshot(environmentID)

	// Release commands run only while a rollout is in flight (the target is
	// not the active revision). A converged environment never re-creates a
	// release Job: drift healing that re-ran migrations spontaneously would
	// turn an explanatory pass into a mutation nobody asked for.
	rolloutInFlight := target.ActiveRevisionID == nil || *target.ActiveRevisionID != *target.TargetRevisionID
	releaseWaiting := make(map[string]string)

	// Environment-scoping objects first: namespace, the values Secret, the
	// ingress isolation, then the shared edge objects no single service
	// owns.
	envOps := []Op{
		{Kind: OpApply, Object: desired.namespace},
		{Kind: OpApply, Object: desired.secret},
		{Kind: OpApply, Object: desired.policy},
	}
	envOps = append(envOps, applyAll(desired.environment)...)
	envChanged, err := k.executeOps(ctx, envOps)
	if err != nil {
		k.journalOpFailure(ctx, attachment, "apply:environment", "Apply environment resources", envChanged, err)
		return 0, err
	}
	if len(envChanged) > 0 {
		attachment.ensure(ctx)
		attachment.completeStep(ctx, "apply:environment", "Apply environment resources",
			journal.StepSucceeded, envChanged)
	}

	// Pre-pass health gates later batches: a dependency that is not ready
	// produces a visible waiting step, not an opaque retry.
	preHealth := healthByService(k.evaluateServices(environmentID, rev, snapshot, intercepts, nil, desired.colors))
	var unhealthyEarlier []string
	for _, batch := range batches {
		blockedOn := strings.Join(unhealthyEarlier, ", ")
		for _, dotted := range batch {
			collection, service := splitService(dotted)
			switch collection {
			case "databases", "buckets":
				// The substrate owns provisioning; the pass only renders
				// the wait visibly and closes the step once outputs exist.
				// Step keys carry the dotted name: bare keys would collide
				// across claim collections.
				noun := "database"
				if collection == "buckets" {
					noun = "bucket"
				}
				stepKey, title := "claim:"+dotted, "Provision "+dotted
				if reason := claimWaiting[dotted]; reason != "" {
					attachment.waitStep(ctx, stepKey, title, reason)
				} else if attachment.adopted() {
					attachment.completeStep(ctx, stepKey, title, journal.StepSucceeded,
						[]string{noun + " provisioned; connection outputs published"})
				}
				// A bucket's edge objects (its route) apply beside the claim
				// step, keyed by the dotted name the renderer labels them
				// with. They render only once the claim allocated a bucket
				// name, and they point at the shared gateway, so applying
				// them ahead of provisioning is harmless and lets the
				// certificate issue while the bucket settles.
				if objs, ok := desired.services[dotted]; ok && len(objs.rest) > 0 {
					serviceChanged, err := k.executeOps(ctx, applyAll(objs.rest))
					if err != nil {
						k.journalOpFailure(ctx, attachment, "apply:"+dotted, "Apply "+dotted, serviceChanged, err)
						return 0, err
					}
					if len(serviceChanged) > 0 {
						attachment.ensure(ctx)
						attachment.completeStep(ctx, "apply:"+dotted, "Apply "+dotted,
							journal.StepSucceeded, serviceChanged)
					}
				}
				continue
			}
			if reason, waits := waiting[dotted]; waits {
				attachment.waitStep(ctx, "apply:"+service, "Apply "+service, reason)
				continue
			}
			if blockedOn != "" {
				attachment.waitStep(ctx, "apply:"+service, "Apply "+service, "waiting for "+blockedOn)
				continue
			}
			objs := desired.services[service]
			if rolloutInFlight && objs.releaseJob != nil {
				state, reason, err := k.ensureRelease(ctx, attachment, target, service, objs.releaseJob, snapshot)
				if err != nil {
					return 0, err
				}
				switch state {
				case releaseRunning:
					releaseWaiting[dotted] = reason
					continue
				case releaseFailed:
					// The promoted revision's release command failed
					// terminally: the rollout cannot proceed. Same policy as
					// an exceeded deadline: the run fails with diagnostics
					// and the target returns to the last active revision
					// when one exists; a first deployment keeps its target
					// so a redeploy retries the release.
					attachment.finish(ctx, journal.RunFailed, service+": "+reason)
					rows, err := k.deps.Deploy.FallbackTargetLocked(ctx, store.FallbackEnvironmentTargetParams{
						EnvironmentID:    environmentID,
						TargetRevisionID: target.TargetRevisionID,
					})
					if err != nil {
						return 0, fmt.Errorf("reconcile: fall back target: %w", err)
					}
					if rows > 0 {
						slog.WarnContext(ctx, "release command failed; target returned to the active revision",
							"environment_id", environmentID, "service", service)
						k.EnqueueFor(environmentID, ReasonFallback)
					}
					return 0, nil
				}
			}
			// The live workload is matched by the rendered Deployment's own
			// name: an application may briefly own several Deployments (one
			// per color during a blue-green switch), and the replica
			// ownership dance must reason about the one being applied.
			var liveWorkload *observe.Object
			if objs.deployment != nil {
				liveWorkload = liveObjectByRef(snapshot, module.KindWorkload,
					objs.deployment.Namespace, objs.deployment.Name)
			}
			plan := desired.plans[service]
			ops := planServiceOps(objs, liveWorkload,
				liveObject(snapshot, service, module.KindAutoscaler),
				plan.held() && plan.pendingReplicas != nil)
			serviceChanged, err := k.executeOps(ctx, ops)
			if err != nil {
				k.journalOpFailure(ctx, attachment, "apply:"+service, "Apply "+service, serviceChanged, err)
				return 0, err
			}
			if len(serviceChanged) > 0 {
				attachment.ensure(ctx)
				attachment.completeStep(ctx, "apply:"+service, "Apply "+service,
					journal.StepSucceeded, serviceChanged)
			}
		}
		for _, dotted := range batch {
			collection, _ := splitService(dotted)
			switch collection {
			case "databases", "buckets":
				// Claim readiness is fresh Postgres truth from ensureClaims;
				// the projection-backed health view governs activation, not
				// this gate. A provisioned claim must never withhold a
				// dependent workload on informer or poll lag.
				if claimWaiting[dotted] != "" {
					unhealthyEarlier = append(unhealthyEarlier, dotted)
				}
			default:
				// A pending release also blocks later batches: the service's
				// old members may look healthy, but its new revision has
				// deliberately not been applied yet.
				if _, waits := waiting[dotted]; waits || releaseWaiting[dotted] != "" || !preHealth[dotted] {
					unhealthyEarlier = append(unhealthyEarlier, dotted)
				}
			}
		}
	}

	// Blue-green switches narrate on the run: a waiting step while the new
	// color starts, a completed step when traffic moves.
	journalTraffic(ctx, attachment, desired.plans, k.cfg.RetireDrain)
	pass.Mark("apply")

	// Prune only with a complete desired set in hand, only stateless kinds,
	// only objects owned by this environment, with UID preconditions. The
	// blue-green color still serving and every retiring color inside its
	// drain window are protected; the drain's remaining time requeues the
	// pass so a converged environment still prunes on time.
	protected, retireRequeue := k.retirements(desired.plans, snapshot, time.Now())
	var pruned []string
	for _, ref := range planPrune(snapshot.Objects, desired.refs, protected) {
		deleted, err := k.deps.Cluster.Delete(ctx, ref)
		if err != nil {
			k.journalOpFailure(ctx, attachment, "prune", "Prune removed objects", pruned, err)
			return 0, err
		}
		if deleted {
			pruned = append(pruned, "deleted "+ref.String())
		}
	}
	if len(pruned) > 0 {
		attachment.ensure(ctx)
		attachment.completeStep(ctx, "prune", "Prune removed objects", journal.StepSucceeded, pruned)
	}

	if err := k.releaseAbsentHostnames(ctx, environmentID); err != nil {
		return 0, err
	}
	pass.Mark("prune")

	// Route certificates come before the health evaluation: the pass's
	// edge verdicts (a domain that does not reach this edge yet) ride into
	// the module through the kernel cache, so a deferred route stops gating
	// health in the same pass that discovered it.
	tls := k.reconcileTLS(ctx, attachment, target, rev, desired, k.deps.Observed.Snapshot(environmentID), probeInterval)
	pass.Mark("tls")

	// Evaluate over a post-apply snapshot and activate when every service of
	// the target revision passes its health conditions on a fresh view.
	statuses := k.evaluateServices(environmentID, rev, k.deps.Observed.Snapshot(environmentID), intercepts, nil, desired.colors)
	// The list rollup reads this verdict instead of projecting status per
	// environment; it is the same evaluation over the same snapshot.
	k.recordHealth(environmentID, statuses, time.Now())
	// A service blocked on a projection the observation never delivered
	// cannot be healed by waiting: the object exists on the cluster but its
	// creation fell into an informer-establishment gap, and no further
	// event will ever arrive for it. Bouncing the watch connections (rate
	// limited inside) makes the reflectors re-list, and the requeue below
	// sees the restored objects.
	if k.deps.RefreshObservation != nil && unobservedDiagnostics(statuses) {
		k.deps.RefreshObservation()
	}
	pass.Mark("health")
	healthy := k.deps.Observed.Source().State == module.SourceFresh
	for _, status := range statuses {
		if status.Health != module.HealthHealthy {
			healthy = false
		}
	}
	if len(releaseWaiting) > 0 {
		// A pending release command means the target revision's workload was
		// deliberately not applied; the previous revision reporting healthy
		// must not activate the new one.
		healthy = false
	}
	if !healthy {
		blocked := make([]string, 0, len(unhealthyEarlier))
		for _, dotted := range unhealthyEarlier {
			reason := claimWaiting[dotted]
			if reason == "" {
				reason = waiting[dotted]
			}
			if reason == "" {
				reason = releaseWaiting[dotted]
			}
			if reason == "" {
				reason = "health pending"
			}
			blocked = append(blocked, dotted+": "+reason)
		}
		slog.Debug("reconcile: pass not healthy", "environment", environmentID,
			"source", k.deps.Observed.Source().State,
			"blocked", strings.Join(blocked, "; "))
	}

	if healthy && !tls.blocked {
		if tls.failed && attachment.created {
			// A converged environment's issuance failed after its domain
			// arrived: the run this pass created to say so closes failed
			// before the pass concludes, since nothing else is wrong.
			attachment.finish(ctx, journal.RunFailed, tls.failure)
		}
		return soonest(retireRequeue, tls.requeue), k.activate(ctx, attachment, target, rev)
	}
	if attachment.created {
		// The healing work is recorded; health recovery arrives via watch
		// events and, if needed, the requeue below.
		status, reason := journal.RunSucceeded, ""
		if tls.failed {
			status, reason = journal.RunFailed, tls.failure
		}
		attachment.finish(ctx, status, reason)
	}
	if attachment.adopted() && rolloutRun(attachment.run.Kind) {
		// Release commands extend the deadline by their own budget: their
		// Jobs enforce the manifest timeouts, so the rollout deadline only
		// needs to cover everything after them.
		if tls.failed || time.Since(target.UpdatedAt) > rolloutBudget(rev.Definition, k.cfg.RolloutDeadline)+releaseBudget(rev.Definition) {
			// Product policy: past the deadline the run fails
			// with diagnostics and the target returns to the last active
			// revision when one exists. The guarded compare-and-swap makes
			// a concurrent newer promotion win; the first deployment of an
			// environment has nothing to fall back to and keeps its
			// target, where level-triggered reconciliation continues and a
			// late recovery still activates.
			attachment.completeStepFields(ctx, "verify", "Verify health", journal.StepFailed,
				[]string{strings.Join(healthSummary(statuses), "\n")}, healthFields(statuses))
			attachment.finish(ctx, journal.RunFailed, rolloutFailure(tls, statuses))
			rows, err := k.deps.Deploy.FallbackTargetLocked(ctx, store.FallbackEnvironmentTargetParams{
				EnvironmentID:    environmentID,
				TargetRevisionID: target.TargetRevisionID,
			})
			if err != nil {
				return 0, fmt.Errorf("reconcile: fall back target: %w", err)
			}
			if rows > 0 {
				slog.WarnContext(ctx, "rollout deadline exceeded; target returned to the active revision",
					"environment_id", environmentID)
				k.EnqueueFor(environmentID, ReasonFallback)
				return 0, nil
			}
			// A first deployment has nothing to fall back to; level-triggered
			// reconciliation continues on the ordinary cadence so a late
			// recovery still activates, instead of the environment silently
			// leaving the queue until the audit.
			return soonest(soonest(requeueHealthCheck, retireRequeue), tls.requeue), nil
		}
		attachment.waitStepFields(ctx, "verify", "Verify health",
			strings.Join(healthSummary(statuses), "\n"), healthFields(statuses))
	}
	return soonest(soonest(requeueHealthCheck, retireRequeue), tls.requeue), nil
}

// soonest picks the shorter of two requeue delays, ignoring zero (none).
// hostGatewayUnavailable marks a desired set blocked on the host gateway
// name not resolving; unlike every other desired-set error it is transient.
type hostGatewayUnavailable struct{ cause error }

func (e *hostGatewayUnavailable) Error() string { return e.cause.Error() }
func (e *hostGatewayUnavailable) Unwrap() error { return e.cause }

// waitHostGateway handles a desired set blocked on the host gateway. On the
// local platform the CoreDNS entry for host.k3d.internal lands after the
// cluster boots (and is repaired by the next skali dev pass when a node
// restart dropped it), so a lookup that fails now succeeds a little later
// and the resolver caches nothing on failure. The pass waits visibly and
// re-picks on the health cadence instead of failing the run. An adopted
// rollout run past its deadline fails as any stalled rollout would; the
// target stays either way, so a late resolution still activates.
// applyIsolation applies the namespace and its ingress isolation on a pass
// that cannot render the target revision: an environment whose active
// revision runs pods must not lose (or, across an upgrade, never gain) its
// isolation because a later target is broken. Both objects render from the
// environment's identity alone. An apply error is returned so the pass
// retries with backoff instead of treating a transient API failure as a
// permanent render failure.
func (k *Kernel) applyIsolation(ctx context.Context, environmentID uuid.UUID, rev *revision.Revision) error {
	id := environmentID.String()
	_, err := k.executeOps(ctx, []Op{
		{Kind: OpApply, Object: rendering.RenderNamespace(rev.Project, rev.Environment, id)},
		{Kind: OpApply, Object: rendering.RenderEnvironmentPolicy(rev.Project, rev.Environment, id)},
	})
	if err != nil {
		return fmt.Errorf("reconcile: apply isolation: %w", err)
	}
	return nil
}

func (k *Kernel) waitHostGateway(ctx context.Context, attachment *runAttachment, environmentID uuid.UUID,
	target store.EnvironmentTarget, rev *revision.Revision, cause error) (time.Duration, error) {
	slog.Warn("reconcile: host gateway unresolved, waiting", "environment", environmentID, "error", cause)
	if attachment.adopted() && rolloutRun(attachment.run.Kind) &&
		time.Since(target.UpdatedAt) > rolloutBudget(rev.Definition, k.cfg.RolloutDeadline) {
		attachment.completeStep(ctx, "render", "Render desired state", journal.StepFailed,
			[]string{"rendering the desired state failed: " + cause.Error(),
				"the host gateway did not resolve within the rollout deadline"})
		attachment.finish(ctx, journal.RunFailed, "the host gateway did not resolve within the rollout deadline: "+cause.Error())
		return requeueHealthCheck, nil
	}
	attachment.waitStep(ctx, "render", "Render desired state",
		"waiting for the host gateway to resolve in the cluster: "+cause.Error())
	return requeueHealthCheck, nil
}

// prepareEdgeVerdicts probes, outside the environment lock, every TLS route
// domain of the target revision whose cached verdict is due, and returns
// the probe cadence the pass runs under: the rollout interval while a
// rollout run is attached, the idle interval otherwise. Best effort: a
// failure here leaves the cache as it was and the locked pass, which
// re-derives everything, requeues quickly on a missing verdict. The
// revision and values it reads stay in inputs for the locked pass.
func (k *Kernel) prepareEdgeVerdicts(ctx context.Context, environmentID uuid.UUID, inputs *passInputs) time.Duration {
	interval := edgeProbeIdleInterval
	if !k.cfg.Certificates || k.deps.ProbeDomain == nil {
		return interval
	}
	state, err := k.deps.Store.GetEnvironmentPass(ctx, environmentID)
	target := state.EnvironmentTarget
	if err != nil || target.TargetRevisionID == nil || target.State != deploy.EnvironmentStateActive {
		return interval
	}
	rev, err := inputs.revision(ctx, *target.TargetRevisionID)
	if err != nil {
		return interval
	}
	if run, ok := adoptableRun(state); ok && rolloutRun(run.Kind) {
		interval = edgeProbeRolloutInterval
	}
	values, err := inputs.values(ctx, rev)
	if err != nil {
		slog.DebugContext(ctx, "edge probe phase skipped", "environment_id", environmentID, "err", err)
		return interval
	}
	routes, err := routeDomains(rev, values)
	if err != nil {
		slog.DebugContext(ctx, "edge probe phase skipped", "environment_id", environmentID, "err", err)
		return interval
	}
	k.probeDue(ctx, environmentID, routes, time.Now(), interval)
	return interval
}

func soonest(a, b time.Duration) time.Duration {
	if b > 0 && (a <= 0 || b < a) {
		return b
	}
	return a
}

// activate moves the active pointer, guarded so a late activation of a
// superseded revision is a no-op, and concludes the attached run.
func (k *Kernel) activate(ctx context.Context, attachment *runAttachment, target store.EnvironmentTarget, rev *revision.Revision) error {
	upToDate := target.ActiveRevisionID != nil && *target.ActiveRevisionID == *target.TargetRevisionID
	if upToDate {
		// Nothing to activate; close a leftover adopted run, if any.
		attachment.finish(ctx, journal.RunSucceeded, "")
		return nil
	}
	rows, err := k.deps.Store.SetEnvironmentActiveRevision(ctx, store.SetEnvironmentActiveRevisionParams{
		EnvironmentID:    target.EnvironmentID,
		ActiveRevisionID: target.TargetRevisionID,
	})
	if err != nil {
		return fmt.Errorf("reconcile: activate revision: %w", err)
	}
	if rows == 0 {
		// A newer target won the race; its own enqueue drives on.
		attachment.completeStep(ctx, "activate", "Activate revision", journal.StepFailed,
			[]string{"activation skipped: the target moved to a newer revision"})
		attachment.finish(ctx, journal.RunCancelled, "")
		return nil
	}
	attachment.ensure(ctx)
	attachment.completeStep(ctx, "verify", "Verify health", journal.StepSucceeded,
		[]string{"all services report healthy"})
	attachment.completeStep(ctx, "activate", "Activate revision", journal.StepSucceeded,
		[]string{"revision " + rev.Checksum + " is active"})
	attachment.finish(ctx, journal.RunSucceeded, "")
	k.deps.Observed.Invalidate(target.EnvironmentID)
	return nil
}

// journalOpFailure records a failed cluster operation. An adopted
// deployment run stays running (the rollout deadline governs its fate); a
// created reconcile run fails immediately.
func (k *Kernel) journalOpFailure(ctx context.Context, attachment *runAttachment, key, title string, done []string, cause error) {
	attachment.ensure(ctx)
	attachment.completeStep(ctx, key, title, journal.StepFailed,
		append(done, "operation failed: "+cause.Error()))
	if attachment.created {
		attachment.finish(ctx, journal.RunFailed, title+" failed: "+cause.Error())
	}
}

// executeOps performs planned operations in order and reports the material
// changes in human-readable form; a no-op pass returns an empty list.
func (k *Kernel) executeOps(ctx context.Context, ops []Op) ([]string, error) {
	var changed []string
	for _, op := range ops {
		switch op.Kind {
		case OpApply:
			result, err := k.deps.Cluster.Apply(ctx, op.Object, op.Force)
			if err != nil {
				return changed, err
			}
			if result.Changed {
				changed = append(changed, "applied "+describeObject(op.Object))
			}
		case OpDelete:
			deleted, err := k.deps.Cluster.Delete(ctx, op.Ref)
			if err != nil {
				return changed, err
			}
			if deleted {
				changed = append(changed, "deleted "+op.Ref.String())
			}
		case OpDisown:
			if err := k.deps.Cluster.DisownFields(ctx, op.Ref, kube.FieldManagerProject, op.Paths...); err != nil {
				return changed, err
			}
			changed = append(changed, "released "+strings.Join(op.Paths, ", ")+" of "+op.Ref.String())
		}
	}
	return changed, nil
}

// desiredSet renders one revision into its full desired state. Secret
// plaintexts are decrypted for the values Secret only and never logged.
// restartedAt is the target's restart stamp; nil means no restart was ever
// forced for this environment.
// loadIntercepts decodes the environment's intercept rows into the declared
// host-port map per application key.
func (k *Kernel) loadIntercepts(ctx context.Context, environmentID uuid.UUID) (map[string]map[string]int32, error) {
	rows, err := k.deps.Store.ListEnvironmentIntercepts(ctx, environmentID)
	if err != nil {
		return nil, fmt.Errorf("reconcile: list intercepts: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	intercepts := make(map[string]map[string]int32, len(rows))
	for _, row := range rows {
		var ports map[string]int32
		if err := json.Unmarshal(row.Ports, &ports); err != nil {
			return nil, fmt.Errorf("reconcile: decode intercept ports for %s: %w", row.ApplicationKey, err)
		}
		intercepts[row.ApplicationKey] = ports
	}
	return intercepts, nil
}

// loadAppRestarts reads the environment's per-application restart stamps as
// UTC RFC3339 strings ready for rendering; nil when nothing was ever
// restarted.
func (k *Kernel) loadAppRestarts(ctx context.Context, environmentID uuid.UUID) (map[string]string, error) {
	rows, err := k.deps.Store.ListEnvironmentRestarts(ctx, environmentID)
	if err != nil {
		return nil, fmt.Errorf("reconcile: list restarts: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	stamps := make(map[string]string, len(rows))
	for _, row := range rows {
		stamps[row.ApplicationKey] = row.RestartedAt.UTC().Format(time.RFC3339)
	}
	return stamps, nil
}

// passInputs holds what one pass would otherwise read twice: the decoded
// target revision and the values it pinned. Both are immutable for a
// revision ID (revisions are never updated, pinned versions never change),
// so the pre-lock probe phase, the render, and the redactor share them,
// while the target itself is still read again under the lock.
type passInputs struct {
	kernel        *Kernel
	environmentID uuid.UUID

	revisionID uuid.UUID
	rev        *revision.Revision
	pinned     map[string]string // rev's pinned plaintexts; nil until read
}

// revision decodes the revision once per pass.
func (in *passInputs) revision(ctx context.Context, id uuid.UUID) (*revision.Revision, error) {
	if in.rev != nil && in.revisionID == id {
		return in.rev, nil
	}
	rev, err := in.kernel.deps.Deploy.GetRevision(ctx, id)
	if err != nil {
		return nil, err
	}
	in.revisionID, in.rev, in.pinned = id, rev, nil
	return rev, nil
}

// values decrypts the versions rev pinned, once per pass. The map is shared;
// callers must not modify it.
func (in *passInputs) values(ctx context.Context, rev *revision.Revision) (map[string]string, error) {
	if in.pinned != nil && rev == in.rev {
		return in.pinned, nil
	}
	values, err := in.kernel.deps.Values.Plaintexts(ctx, in.environmentID, secretVersions(rev))
	if err != nil {
		return nil, err
	}
	if rev == in.rev {
		in.pinned = values
	}
	return values, nil
}

// desiredSet renders the target revision for the environment. priority is
// the environment's live setting (normal or high); it selects the
// PriorityClass of every application pod and, like the restart stamps, is
// not part of the revision.
// secretVersions maps the revision's secret references to their stored
// versions: the values Secret read set and the per-application values
// identity share it.
func secretVersions(rev *revision.Revision) map[string]int {
	refs := make(map[string]int, len(rev.Secrets))
	for name, secret := range rev.Secrets {
		refs[name] = secret.Version
	}
	return refs
}

// renderInputs builds the render options that shape application pod
// templates, and therefore blue-green colors: prepared images and
// platforms, secret versions, restart stamps, priority, cluster mode.
// desiredSet adds the variables and intercept routing on top; the status
// projection needs exactly these to name each application's desired color
// without decrypting anything.
func (k *Kernel) renderInputs(environmentID uuid.UUID, rev *revision.Revision, restartedAt *time.Time,
	appRestarts map[string]string, generations map[string]string, intercepts map[string]map[string]int32,
	priority string) (rendering.Options, error) {
	buildImages := map[string]string{}
	appPlatforms := map[string][]string{}
	for key, application := range rev.Definition.Applications {
		if _, ok := intercepts[key]; ok {
			// Intercepted applications ship no artifact; they render no
			// workload either.
			continue
		}
		artifact, resolved := rev.Artifacts[key]
		if application.Source.Kind != "build" {
			// Image-sourced applications still carry their declared
			// platforms on the artifact for arch-affinity rendering.
			if resolved && len(artifact.Platforms) > 0 {
				appPlatforms[key] = artifact.Platforms
			}
			continue
		}
		if !resolved {
			return rendering.Options{}, fmt.Errorf("reconcile: revision has no artifact for application %s", key)
		}
		image := artifact.Reference
		if artifact.Digest != "" {
			image += "@" + artifact.Digest
		}
		buildImages[key] = image
		if len(artifact.Platforms) > 0 {
			appPlatforms[key] = artifact.Platforms
		}
	}
	options := rendering.Options{
		Namespace:               rendering.RenderNamespace(rev.Project, rev.Environment, environmentID.String()).Name,
		BuildImages:             buildImages,
		AppPlatforms:            appPlatforms,
		EnvironmentID:           environmentID.String(),
		RevisionChecksum:        rev.Checksum,
		SecretVersions:          secretVersions(rev),
		ProgressDeadlineSeconds: int64(k.cfg.RolloutDeadline / time.Second),
		ManagedCluster:          k.cfg.ManagedCluster,
		StorageClass:            k.cfg.StorageClass,
		Certificates:            k.cfg.Certificates,
		Intercepts:              intercepts,
		PriorityClassName:       layout.PriorityClassFor(priority),
		AppRestartedAt:          appRestarts,
		OutputGenerations:       generations,
	}
	if restartedAt != nil {
		options.RestartedAt = restartedAt.UTC().Format(time.RFC3339)
	}
	return options, nil
}

func (k *Kernel) desiredSet(ctx context.Context, inputs *passInputs, rev *revision.Revision,
	restartedAt *time.Time, appRestarts map[string]string, generations map[string]string, bucketNames map[string]string,
	intercepts map[string]map[string]int32, priority string, snapshot observe.Snapshot) (*desiredSet, error) {
	environmentID := inputs.environmentID
	variables, err := inputs.values(ctx, rev)
	if err != nil {
		return nil, err
	}
	data, err := rendering.EnvironmentSecretData(rev.Definition, variables)
	if err != nil {
		return nil, err
	}

	namespace := rendering.RenderNamespace(rev.Project, rev.Environment, environmentID.String())
	secret := rendering.RenderEnvironmentSecret(rev.Project, rev.Environment,
		environmentID.String(), rev.Checksum, data)
	policy := rendering.RenderEnvironmentPolicy(rev.Project, rev.Environment, environmentID.String())

	renderOptions, err := k.renderInputs(environmentID, rev, restartedAt, appRestarts, generations, intercepts, priority)
	if err != nil {
		return nil, err
	}
	renderOptions.Variables = variables
	renderOptions.BucketNames = bucketNames
	renderOptions.WaitImage = k.releaseWaitImage(ctx, rev.Definition)
	// Bucket routes resolve here, against the same values the render
	// sees, so the hostname the claim records is the one the edge serves.
	bucketRoutes, err := resolveBucketRoutes(rev.Definition, variables)
	if err != nil {
		return nil, err
	}

	// Intercept declarations are keyed by manifest port name; rendering
	// needs them per rendered service port. A failure here is permanent for
	// this target and takes the safe desiredSet-error path: journaled, and
	// nothing is ever pruned on it.
	var interceptPorts map[string]map[string]int32
	interceptHostIP := ""
	if len(intercepts) > 0 {
		if k.deps.HostGateway == nil {
			return nil, fmt.Errorf("reconcile: intercepts require a host gateway resolver")
		}
		hostIP, err := k.deps.HostGateway(ctx)
		if err != nil {
			return nil, &hostGatewayUnavailable{cause: fmt.Errorf("reconcile: resolve host gateway: %w", err)}
		}
		interceptHostIP = hostIP
		interceptPorts = make(map[string]map[string]int32, len(intercepts))
		for key, declared := range intercepts {
			application, ok := rev.Definition.Applications[key]
			if !ok {
				// A stale row for an application the revision no longer
				// declares; nothing renders for it either way.
				continue
			}
			resolved, err := rendering.ResolveInterceptPorts(application, declared)
			if err != nil {
				return nil, fmt.Errorf("reconcile: intercept %s: %w", key, err)
			}
			interceptPorts[key] = resolved
		}
	}

	renderOptions.Intercepts = interceptPorts
	renderOptions.InterceptHostIP = interceptHostIP
	result := &compiler.Result{Hash: rev.DefinitionHash, Definition: rev.Definition}
	// Colors come from the same options the render sees, so the traffic
	// decision and the rendered selectors can never disagree.
	colors, err := rendering.ApplicationColors(result, renderOptions)
	if err != nil {
		return nil, err
	}
	plans := planTraffic(rev, colors, snapshot, namespace.Name, intercepts)
	renderOptions.TrafficColors, renderOptions.PendingReplicas = trafficOptions(plans)
	objects, err := rendering.Render(result, renderOptions)
	if err != nil {
		return nil, err
	}
	if err := rendering.ValidateObjects(append([]runtime.Object{namespace, secret, policy}, objects...)); err != nil {
		return nil, err
	}
	services, refsList, certDomains, err := groupObjects(objects)
	if err != nil {
		return nil, err
	}
	refsList = append(refsList,
		kube.ObjectRef{GVK: namespace.GroupVersionKind(), Name: namespace.Name},
		kube.ObjectRef{GVK: secret.GroupVersionKind(), Namespace: secret.Namespace, Name: secret.Name},
		kube.ObjectRef{GVK: policy.GroupVersionKind(), Namespace: policy.Namespace, Name: policy.Name},
	)
	// Objects rendered without a service label (the shared redirect
	// Middleware) belong to the environment pass, not to any batch.
	shared := services[""]
	delete(services, "")
	return &desiredSet{namespace: namespace, secret: secret, policy: policy,
		environment: shared.rest, services: services, refs: refsList,
		colors: colors, plans: plans, certDomains: certDomains, bucketRoutes: bucketRoutes}, nil
}

// resolveBucketRoutes projects the resolved hostname list onto the routed
// buckets, keyed by bucket key, for the claim manager.
func resolveBucketRoutes(definition compiler.ProjectDefinition, variables map[string]string) (map[string]BucketRoute, error) {
	resolved, err := compiler.ResolveRoutes(definition, variables)
	if err != nil {
		return nil, err
	}
	routes := make(map[string]BucketRoute)
	for _, route := range resolved {
		if route.Bucket == "" {
			continue
		}
		routes[route.Bucket] = BucketRoute{Domain: route.Domain, TLS: definition.Buckets[route.Bucket].Route.TLS}
	}
	return routes, nil
}

// ensureClaims records the revision's infrastructure claims (databases and
// buckets) through the claim manager and returns the dotted-name waiting
// reasons for every claim that is not provisioned. Without a substrate
// every claim-backed service waits visibly.
func (k *Kernel) ensureClaims(ctx context.Context, projectID, environmentID uuid.UUID, rev *revision.Revision, bucketRoutes map[string]BucketRoute) (map[string]string, error) {
	total := len(rev.Definition.Databases) + len(rev.Definition.Buckets)
	if total == 0 {
		return nil, nil
	}
	claimWaiting := make(map[string]string, total)
	if k.deps.Claims == nil {
		for key := range rev.Definition.Databases {
			claimWaiting["databases."+key] = "the platform substrate is not available"
		}
		for key := range rev.Definition.Buckets {
			claimWaiting["buckets."+key] = "the platform substrate is not available"
		}
		return claimWaiting, nil
	}
	states, err := k.deps.Claims.Ensure(ctx, ClaimEnsureInput{
		ProjectID:     projectID,
		EnvironmentID: environmentID,
		Revision:      rev,
		BucketRoutes:  bucketRoutes,
	})
	if err != nil {
		return nil, fmt.Errorf("reconcile: ensure claims: %w", err)
	}
	for _, state := range states {
		if state.Provisioned {
			continue
		}
		reason := state.Waiting
		if reason == "" {
			reason = "waiting for the platform substrate"
		}
		claimWaiting[state.Service] = reason
	}
	return claimWaiting, nil
}

// redactor covers the environment's current values plus, when a revision is
// given, the exact versions it pinned (a tombstoned value is no longer
// current but still resolvable by an old revision). Values a route domain
// references are exempt: a hostname the edge serves is public by
// construction (the status projection resolves it for the same reason),
// and the TLS checkpoints must be able to name it. Kernel log lines carry
// no other values, so this is defense in depth, not the only barrier. The
// pinned values come from the pass's inputs, which the render shares.
func (k *Kernel) redactor(ctx context.Context, environmentID uuid.UUID, rev *revision.Revision, inputs *passInputs) *redact.Redactor {
	redactor, err := k.deps.Values.Redactor(ctx, environmentID, uuid.Nil)
	if err != nil {
		slog.Warn("build redactor", "environment", environmentID, "error", err)
		redactor = redact.New(nil)
	}
	if rev == nil {
		return redactor
	}
	plaintexts, err := inputs.values(ctx, rev)
	if err != nil {
		slog.Warn("build pinned redactor", "environment", environmentID, "error", err)
		return redactor.Without(routeVariableNames(rev))
	}
	byPlaintext := make(map[string]string, len(plaintexts))
	for name, value := range plaintexts {
		byPlaintext[value] = name
	}
	redactor = redactor.Merge(redact.New(byPlaintext))
	return redactor.Without(routeVariableNames(rev))
}

// routeVariableNames lists the project variables any route domain of the
// revision references.
func routeVariableNames(rev *revision.Revision) map[string]bool {
	names := map[string]bool{}
	for _, domain := range routeDomainExpressions(rev.Definition) {
		for _, part := range domain.Parts {
			if part.Kind == "project_variable" {
				names[part.Name] = true
			}
		}
	}
	return names
}

// routeDomainExpressions lists every hostname expression the edge serves
// for the definition: application routes and bucket routes alike.
func routeDomainExpressions(definition compiler.ProjectDefinition) []compiler.Expression {
	var domains []compiler.Expression
	for _, application := range definition.Applications {
		for _, route := range application.Routes {
			domains = append(domains, route.Domain)
		}
	}
	for _, bucket := range definition.Buckets {
		if bucket.Route != nil {
			domains = append(domains, bucket.Route.Domain)
		}
	}
	return domains
}

// liveObjectByRef finds one observed object of a kind by namespace and
// name, for kinds an application may own more than once.
func liveObjectByRef(snapshot observe.Snapshot, kind, namespace, name string) *observe.Object {
	for index := range snapshot.Objects {
		obj := &snapshot.Objects[index]
		if obj.Kind == kind && obj.Ref.Namespace == namespace && obj.Ref.Name == name {
			return obj
		}
	}
	return nil
}

func liveObject(snapshot observe.Snapshot, service, kind string) *observe.Object {
	for index := range snapshot.Objects {
		obj := &snapshot.Objects[index]
		if obj.Service == service && obj.Kind == kind {
			return obj
		}
	}
	return nil
}

// healthByService keys health by dotted service name; bare keys may repeat
// across collections.
// unobservedDiagnostics reports whether any service is blocked on a
// projection the observation plane has not delivered: the modules'
// *-unobserved codes (pool, tenant, bucket), which only fire after the
// substrate confirmed the object exists, so a missing projection is an
// observation gap rather than propagation delay. The app module's
// missing-resource is deliberately excluded: it appears transiently on
// every normal apply until the watch delivers the new workload, and
// bouncing the connections for that would churn on every deploy.
func unobservedDiagnostics(statuses []ServiceStatus) bool {
	for _, status := range statuses {
		for _, diagnostic := range status.Diagnostics {
			if strings.HasSuffix(diagnostic.Code, "-unobserved") {
				return true
			}
		}
	}
	return false
}

func healthByService(statuses []ServiceStatus) map[string]bool {
	health := make(map[string]bool, len(statuses))
	for _, status := range statuses {
		health[dottedName(status.Type, status.Key)] = status.Health == module.HealthHealthy
	}
	return health
}

func dottedName(serviceType, key string) string {
	switch serviceType {
	case "application":
		return "applications." + key
	case "database":
		return "databases." + key
	case "bucket":
		return "buckets." + key
	}
	return serviceType + "." + key
}

func healthSummary(statuses []ServiceStatus) []string {
	lines := make([]string, 0, len(statuses))
	for _, status := range statuses {
		line := status.Key + ": " + string(status.Health)
		if len(status.Diagnostics) > 0 {
			if strings.HasPrefix(status.Diagnostics[0].Code, "certificate-") {
				line += " (see TLS certificate checkpoint)"
			} else {
				line += " (" + status.Diagnostics[0].Message + ")"
			}
		}
		lines = append(lines, line)
	}
	sort.Strings(lines)
	return lines
}

// rolloutFailure is the one-line summary of a rollout that ran out of
// deadline: the TLS failure when issuance is what failed, otherwise the
// services that are not healthy. The verify step's log keeps the full
// per-service summary, whose shape clients parse.
func rolloutFailure(tls tlsOutcome, statuses []ServiceStatus) string {
	if tls.failed && tls.failure != "" {
		return tls.failure
	}
	unhealthy := make([]ServiceStatus, 0, len(statuses))
	for _, status := range statuses {
		if status.Health != module.HealthHealthy {
			unhealthy = append(unhealthy, status)
		}
	}
	if len(unhealthy) == 0 {
		return "rollout deadline exceeded"
	}
	return "rollout deadline exceeded: " + strings.Join(healthSummary(unhealthy), "; ")
}

// healthFields is the structured twin of healthSummary: one record per
// service so clients can render the snapshot as a status list instead of
// parsing the readable lines. The certificate diagnostics point at the TLS
// checkpoint like the summary does.
func healthFields(statuses []ServiceStatus) map[string]any {
	services := make([]map[string]any, 0, len(statuses))
	for _, status := range statuses {
		record := map[string]any{"key": status.Key, "type": status.Type, "health": string(status.Health)}
		if len(status.Diagnostics) > 0 {
			diagnostic := status.Diagnostics[0]
			record["code"] = diagnostic.Code
			record["severity"] = diagnostic.Severity
			if strings.HasPrefix(diagnostic.Code, "certificate-") {
				record["message"] = "waiting for the TLS certificate"
			} else {
				record["message"] = diagnostic.Message
			}
		}
		services = append(services, record)
	}
	sort.Slice(services, func(i, j int) bool { return services[i]["key"].(string) < services[j]["key"].(string) })
	return map[string]any{"health": true, "services": services}
}

func describeObject(obj runtime.Object) string {
	kind := obj.GetObjectKind().GroupVersionKind().Kind
	accessor, err := meta.Accessor(obj)
	if err != nil {
		return kind
	}
	if accessor.GetNamespace() == "" {
		return kind + "/" + accessor.GetName()
	}
	return kind + "/" + accessor.GetNamespace() + "/" + accessor.GetName()
}

func (k *Kernel) releaseAbsentHostnames(ctx context.Context, env uuid.UUID) error {
	claims, err := k.deps.Store.ListEnvironmentHostnames(ctx, &env)
	if err != nil {
		return err
	}
	if len(claims) == 0 {
		return nil
	}

	if k.deps.LiveRouteHosts == nil {
		return nil
	} // no cluster evidence: retain claims
	live, err := k.deps.LiveRouteHosts(ctx, env)
	if err != nil {
		return err
	}
	return k.deps.Deploy.ReleaseAbsentHostnames(ctx, env, live)
}

// claimOutputs reads the output generations and bucket names of the
// environment's claims; without a substrate there are none.
func (k *Kernel) claimOutputs(ctx context.Context, environmentID uuid.UUID) (ClaimOutputs, error) {
	if k.deps.Claims == nil {
		return ClaimOutputs{}, nil
	}
	outputs, err := k.deps.Claims.Outputs(ctx, environmentID)
	if err != nil {
		return ClaimOutputs{}, fmt.Errorf("reconcile: claim outputs: %w", err)
	}
	return outputs, nil
}
