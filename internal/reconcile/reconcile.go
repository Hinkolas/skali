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
	corev1 "k8s.io/api/core/v1"
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

	desired, err := k.desiredSet(ctx, environmentID, rev)
	if err != nil {
		// An unrenderable revision is permanent for this target: journal the
		// diagnostic, never prune (compiler-error absence must not delete
		// anything), and wait for a new target instead of spinning.
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

	snapshot := k.deps.Observed.Snapshot(environmentID)

	// Environment-scoping objects first: namespace, then the values Secret
	// and (on existing clusters) the registry pull secret.
	envOps := []Op{
		{Kind: OpApply, Object: desired.namespace},
		{Kind: OpApply, Object: desired.secret},
	}
	if desired.pullSecret != nil {
		envOps = append(envOps, Op{Kind: OpApply, Object: desired.pullSecret})
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
		for _, service := range batch {
			if reason, waits := waiting[service]; waits {
				attachment.waitStep(ctx, "apply:"+service, "Apply "+service, reason)
				continue
			}
			if blockedOn != "" {
				attachment.waitStep(ctx, "apply:"+service, "Apply "+service, "waiting for "+blockedOn)
				continue
			}
			ops := planServiceOps(desired.services[service],
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
		for _, service := range batch {
			if _, waits := waiting[service]; waits || !preHealth[service] {
				unhealthyEarlier = append(unhealthyEarlier, service)
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

	if healthy {
		return 0, k.activate(ctx, attachment, target, rev)
	}
	if attachment.created {
		// The healing work is recorded; health recovery arrives via watch
		// events and, if needed, the requeue below.
		attachment.finish(ctx, journal.RunSucceeded)
	}
	if attachment.adopted() && attachment.run.Kind == "deployment" {
		if time.Since(target.UpdatedAt) > k.cfg.RolloutDeadline {
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
			}
			return 0, nil
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
func (k *Kernel) desiredSet(ctx context.Context, environmentID uuid.UUID, rev *revision.Revision) (*desiredSet, error) {
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
	var pullSecret *corev1.Secret
	if k.cfg.PullSecret != nil {
		pullSecret = rendering.RenderPullSecret(rev.Project, rev.Environment,
			environmentID.String(), rev.Checksum,
			k.cfg.PullSecret.Host, k.cfg.PullSecret.Username, k.cfg.PullSecret.Password)
	}

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
		IngressClassName: k.cfg.IngressClassName,
		ManagedCluster:   k.cfg.ManagedCluster,
	}
	if pullSecret != nil {
		renderOptions.ImagePullSecretName = pullSecret.Name
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
	if pullSecret != nil {
		refsList = append(refsList, kube.ObjectRef{
			GVK: pullSecret.GroupVersionKind(), Namespace: pullSecret.Namespace, Name: pullSecret.Name,
		})
	}
	return &desiredSet{namespace: namespace, secret: secret, pullSecret: pullSecret,
		services: services, refs: refsList}, nil
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

func healthByService(statuses []ServiceStatus) map[string]bool {
	health := make(map[string]bool, len(statuses))
	for _, status := range statuses {
		health[status.Key] = status.Health == module.HealthHealthy
	}
	return health
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
