package reconcile

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
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
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/redact"
	"github.com/Hinkolas/skali/internal/revision"
	"github.com/Hinkolas/skali/internal/store"
)

// requeueHealthCheck is the safety interval while waiting on health; watch
// events normally beat it.
const requeueHealthCheck = 15 * time.Second

// reconcileEnvironment is one level-triggered pass: load the target
// revision, render the desired state, apply and prune idempotently,
// evaluate health, and activate when the revision's health conditions pass.
// A pass that changes nothing writes no journal rows.
func (k *Kernel) reconcileEnvironment(ctx context.Context, environmentID uuid.UUID) (time.Duration, error) {
	target, err := k.deps.Store.GetEnvironmentTarget(ctx, environmentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil // environment deleted; the audit reports orphans
		}
		return 0, fmt.Errorf("reconcile: get target: %w", err)
	}
	if target.State != deploy.EnvironmentStateActive {
		// A persisted destructive decision replaces convergence entirely:
		// desired state is absence.
		return k.teardownEnvironment(ctx, environmentID, target)
	}
	if target.TargetRevisionID == nil {
		return 0, nil
	}
	env, err := k.deps.Store.GetEnvironmentByID(ctx, environmentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("reconcile: get environment: %w", err)
	}
	rev, err := k.deps.Deploy.GetRevision(ctx, *target.TargetRevisionID)
	if err != nil {
		return 0, fmt.Errorf("reconcile: load target revision: %w", err)
	}

	attachment := k.attachRun(ctx, environmentID, env.ProjectID, k.redactor(ctx, environmentID))

	desired, err := k.desiredSet(ctx, environmentID, rev, target.RestartedAt)
	if err != nil {
		// An unrenderable revision is permanent for this target: journal the
		// diagnostic, never prune (compiler-error absence must not delete
		// anything), and wait for a new target instead of spinning. The
		// kernel's resync ticker re-picks the environment while target and
		// active disagree, so a later fix lands within one interval.
		slog.Warn("reconcile: desired state failed", "environment", environmentID, "error", err)
		attachment.completeStep(ctx, "render", "Render desired state", journal.StepFailed,
			[]string{"rendering the desired state failed: " + err.Error()})
		if attachment.adopted() {
			attachment.finish(ctx, journal.RunFailed)
		}
		return 0, nil
	}
	batches, waiting, err := planBatches(rev.Definition)
	if err != nil {
		slog.Warn("reconcile: ordering failed", "environment", environmentID, "error", err)
		attachment.completeStep(ctx, "render", "Render desired state", journal.StepFailed,
			[]string{"ordering services failed: " + err.Error()})
		if attachment.adopted() {
			attachment.finish(ctx, journal.RunFailed)
		}
		return 0, nil
	}

	// Desired claims are recorded before health is read, so the projections
	// the evaluation sees are at least as fresh as this pass's intent. The
	// substrate provisions asynchronously; states carry readiness and the
	// visible waiting reasons.
	claimWaiting, err := k.ensureClaims(ctx, env.ProjectID, environmentID, rev)
	if err != nil {
		k.journalOpFailure(ctx, attachment, "claims", "Record database claims", nil, err)
		return 0, err
	}

	snapshot := k.deps.Observed.Snapshot(environmentID)

	// Release commands run only while a rollout is in flight (the target is
	// not the active revision). A converged environment never re-creates a
	// release Job: drift healing that re-ran migrations spontaneously would
	// turn an explanatory pass into a mutation nobody asked for.
	rolloutInFlight := target.ActiveRevisionID == nil || *target.ActiveRevisionID != *target.TargetRevisionID
	releaseWaiting := make(map[string]string)

	// Environment-scoping objects first: namespace, then the values Secret.
	envOps := []Op{
		{Kind: OpApply, Object: desired.namespace},
		{Kind: OpApply, Object: desired.secret},
	}
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
	preHealth := healthByService(k.evaluateServices(rev, snapshot))
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
					attachment.finish(ctx, journal.RunFailed)
					rows, err := k.deps.Store.FallbackEnvironmentTarget(ctx, store.FallbackEnvironmentTargetParams{
						EnvironmentID:    environmentID,
						TargetRevisionID: target.TargetRevisionID,
					})
					if err != nil {
						return 0, fmt.Errorf("reconcile: fall back target: %w", err)
					}
					if rows > 0 {
						slog.WarnContext(ctx, "release command failed; target returned to the active revision",
							"environment_id", environmentID, "service", service)
						k.Enqueue(environmentID)
					}
					return 0, nil
				}
			}
			ops := planServiceOps(objs,
				liveObject(snapshot, service, module.KindWorkload),
				liveObject(snapshot, service, module.KindAutoscaler))
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

	// Prune only with a complete desired set in hand, only stateless kinds,
	// only objects owned by this environment, with UID preconditions.
	var pruned []string
	for _, ref := range planPrune(snapshot.Objects, desired.refs) {
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

	// Evaluate over a post-apply snapshot and activate when every service of
	// the target revision passes its health conditions on a fresh view.
	statuses := k.evaluateServices(rev, k.deps.Observed.Snapshot(environmentID))
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

	if healthy {
		return 0, k.activate(ctx, attachment, target, rev)
	}
	if attachment.created {
		// The healing work is recorded; health recovery arrives via watch
		// events and, if needed, the requeue below.
		attachment.finish(ctx, journal.RunSucceeded)
	}
	if attachment.adopted() && attachment.run.Kind == "deployment" {
		// Release commands extend the deadline by their own budget: their
		// Jobs enforce the manifest timeouts, so the rollout deadline only
		// needs to cover everything after them.
		if time.Since(target.UpdatedAt) > k.cfg.RolloutDeadline+releaseBudget(rev.Definition) {
			// Section 8.4 product policy: past the deadline the run fails
			// with diagnostics and the target returns to the last active
			// revision when one exists. The guarded compare-and-swap makes
			// a concurrent newer promotion win; the first deployment of an
			// environment has nothing to fall back to and keeps its
			// target, where level-triggered reconciliation continues and a
			// late recovery still activates.
			attachment.completeStep(ctx, "verify", "Verify health", journal.StepFailed,
				healthSummary(statuses))
			attachment.finish(ctx, journal.RunFailed)
			rows, err := k.deps.Store.FallbackEnvironmentTarget(ctx, store.FallbackEnvironmentTargetParams{
				EnvironmentID:    environmentID,
				TargetRevisionID: target.TargetRevisionID,
			})
			if err != nil {
				return 0, fmt.Errorf("reconcile: fall back target: %w", err)
			}
			if rows > 0 {
				slog.WarnContext(ctx, "rollout deadline exceeded; target returned to the active revision",
					"environment_id", environmentID)
				k.Enqueue(environmentID)
				return 0, nil
			}
			// A first deployment has nothing to fall back to; level-triggered
			// reconciliation continues on the ordinary cadence so a late
			// recovery still activates, instead of the environment silently
			// leaving the queue until the audit.
			return requeueHealthCheck, nil
		}
		attachment.waitStep(ctx, "verify", "Verify health",
			strings.Join(healthSummary(statuses), "; "))
	}
	return requeueHealthCheck, nil
}

// activate moves the active pointer, guarded so a late activation of a
// superseded revision is a no-op, and concludes the attached run.
func (k *Kernel) activate(ctx context.Context, attachment *runAttachment, target store.EnvironmentTarget, rev *revision.Revision) error {
	upToDate := target.ActiveRevisionID != nil && *target.ActiveRevisionID == *target.TargetRevisionID
	if upToDate {
		// Nothing to activate; close a leftover adopted run, if any.
		attachment.finish(ctx, journal.RunSucceeded)
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
		attachment.finish(ctx, journal.RunCancelled)
		return nil
	}
	attachment.ensure(ctx)
	attachment.completeStep(ctx, "verify", "Verify health", journal.StepSucceeded,
		[]string{"all services report healthy"})
	attachment.completeStep(ctx, "activate", "Activate revision", journal.StepSucceeded,
		[]string{"revision " + rev.Checksum + " is active"})
	attachment.finish(ctx, journal.RunSucceeded)
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
		attachment.finish(ctx, journal.RunFailed)
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
func (k *Kernel) desiredSet(ctx context.Context, environmentID uuid.UUID, rev *revision.Revision,
	restartedAt *time.Time) (*desiredSet, error) {
	refs := make(map[string]int, len(rev.Secrets))
	for name, secret := range rev.Secrets {
		refs[name] = secret.Version
	}
	plaintexts, err := k.deps.Values.SecretPlaintexts(ctx, environmentID, refs)
	if err != nil {
		return nil, err
	}
	variables := maps.Clone(rev.Values)
	if variables == nil {
		variables = map[string]string{}
	}
	maps.Copy(variables, plaintexts)
	data := make(map[string][]byte, len(rev.Values)+len(plaintexts))
	for name, value := range rev.Values {
		data[name] = []byte(value)
	}
	for name, value := range plaintexts {
		data[name] = []byte(value)
	}

	namespace := rendering.RenderNamespace(rev.Project, rev.Environment, environmentID.String())
	secret := rendering.RenderEnvironmentSecret(rev.Project, rev.Environment,
		environmentID.String(), rev.Checksum, data)

	buildImages := map[string]string{}
	for key, application := range rev.Definition.Applications {
		if application.Source.Kind != "build" {
			continue
		}
		artifact, resolved := rev.Artifacts[key]
		if !resolved {
			return nil, fmt.Errorf("reconcile: revision has no artifact for application %s", key)
		}
		image := artifact.Reference
		if artifact.Digest != "" {
			image += "@" + artifact.Digest
		}
		buildImages[key] = image
	}

	renderOptions := rendering.Options{
		Namespace:        namespace.Name,
		Variables:        variables,
		BuildImages:      buildImages,
		EnvironmentID:    environmentID.String(),
		RevisionChecksum: rev.Checksum,
		ManagedCluster:   k.cfg.ManagedCluster,
	}
	if restartedAt != nil {
		renderOptions.RestartedAt = restartedAt.UTC().Format(time.RFC3339)
	}
	objects, err := rendering.Render(
		&compiler.Result{Hash: rev.DefinitionHash, Definition: rev.Definition},
		renderOptions)
	if err != nil {
		return nil, err
	}
	services, refsList, err := groupObjects(objects)
	if err != nil {
		return nil, err
	}
	refsList = append(refsList,
		kube.ObjectRef{GVK: namespace.GroupVersionKind(), Name: namespace.Name},
		kube.ObjectRef{GVK: secret.GroupVersionKind(), Namespace: secret.Namespace, Name: secret.Name},
	)
	return &desiredSet{namespace: namespace, secret: secret,
		services: services, refs: refsList}, nil
}

// ensureClaims records the revision's infrastructure claims (databases and
// buckets) through the claim manager and returns the dotted-name waiting
// reasons for every claim that is not provisioned. Without a substrate
// every claim-backed service waits visibly.
func (k *Kernel) ensureClaims(ctx context.Context, projectID, environmentID uuid.UUID, rev *revision.Revision) (map[string]string, error) {
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

// redactor covers the environment's current secrets; kernel log lines carry
// no values, so this is defense in depth, not the only barrier.
func (k *Kernel) redactor(ctx context.Context, environmentID uuid.UUID) *redact.Redactor {
	redactor, err := k.deps.Values.Redactor(ctx, environmentID, uuid.Nil)
	if err != nil {
		slog.Warn("build redactor", "environment", environmentID, "error", err)
		return redact.New(nil)
	}
	return redactor
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
			line += " (" + status.Diagnostics[0].Message + ")"
		}
		lines = append(lines, line)
	}
	sort.Strings(lines)
	return lines
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
