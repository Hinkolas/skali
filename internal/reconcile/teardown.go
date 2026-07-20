package reconcile

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Hinkolas/skali/internal/deploy"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/kube"
	rendering "github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/store"
)

// workloadKinds are the observed kinds a plain down removes, in delete
// order: the autoscaler first so nothing fights the shrinking Deployment,
// then the workload, then its exposure. Pods are never deleted directly;
// they die with their Deployment and their disappearance is the completion
// signal.
var workloadKinds = []string{
	module.KindAutoscaler,
	module.KindWorkload,
	module.KindService,
	module.KindIngress,
}

// teardownEnvironment executes a persisted destructive decision as one
// level-triggered pass: desired state is absence. Down removes the runtime
// workloads and the values Secret but keeps the namespace and its volumes;
// releasing removes everything and ends by deleting the environment row
// itself. Deletes carry the observed UID as precondition, absence counts as
// done, and watch delete events drive the pass to completion.
func (k *Kernel) teardownEnvironment(ctx context.Context, environmentID uuid.UUID, target store.EnvironmentTarget) (time.Duration, error) {
	env, err := k.deps.Store.GetEnvironmentByID(ctx, environmentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil // the row is already gone; nothing left to explain
		}
		return 0, fmt.Errorf("reconcile: get environment: %w", err)
	}
	releasing := target.State == deploy.EnvironmentStateReleasing
	attachment := k.attachRun(ctx, environmentID, env.ProjectID, k.redactor(ctx, environmentID))
	attachment.ensureKind = "teardown"

	snapshot := k.deps.Observed.Snapshot(environmentID)
	if teardownSettled(snapshot, releasing) {
		if !releasing {
			attachment.finish(ctx, journal.RunSucceeded)
			return 0, nil
		}
		// Purge epilogue: conclude the run first, because deleting the row
		// cascades the journal away with everything else. The journal is
		// explanatory only, so losing it changes nothing.
		attachment.finish(ctx, journal.RunSucceeded)
		if err := k.deps.Deploy.DeleteEnvironment(ctx, environmentID); err != nil {
			return 0, fmt.Errorf("reconcile: delete released environment: %w", err)
		}
		k.deps.Observed.Invalidate(environmentID)
		return 0, nil
	}

	// Runtime workloads, grouped per service for the journal.
	for _, service := range teardownServices(snapshot) {
		var deleted []string
		for _, kind := range workloadKinds {
			for _, obj := range snapshot.Objects {
				if obj.Service != service || obj.Kind != kind {
					continue
				}
				removed, err := k.deps.Cluster.Delete(ctx, obj.Ref)
				if err != nil {
					k.journalOpFailure(ctx, attachment, "delete:"+service, "Remove "+service, deleted, err)
					return 0, err
				}
				if removed {
					deleted = append(deleted, "deleted "+obj.Ref.String())
				}
			}
		}
		if len(deleted) > 0 {
			attachment.ensure(ctx)
			attachment.completeStep(ctx, "delete:"+service, "Remove "+service, journal.StepSucceeded, deleted)
		}
	}

	// The values Secret is rendered, never observed; delete it by its fixed
	// name inside the observed namespace. Without an observed namespace
	// nothing was ever created, so there is no Secret either.
	var envDeleted []string
	namespace := observedNamespace(snapshot)
	if namespace != nil {
		secretRef := kube.ObjectRef{
			GVK:       schema.GroupVersionKind{Version: "v1", Kind: "Secret"},
			Namespace: namespace.Ref.Name,
			Name:      rendering.EnvironmentSecretName,
		}
		removed, err := k.deps.Cluster.Delete(ctx, secretRef)
		if err != nil {
			k.journalOpFailure(ctx, attachment, "delete:environment", "Remove environment resources", envDeleted, err)
			return 0, err
		}
		if removed {
			envDeleted = append(envDeleted, "deleted "+secretRef.String())
		}
	}

	if releasing {
		// Claim teardown slots in here once databases and buckets render
		// cluster state (R5/R6); today there is nothing to release.
		k.teardownClaims(ctx, environmentID)

		var volumes []string
		for _, obj := range snapshot.Objects {
			if obj.Kind != module.KindVolume {
				continue
			}
			removed, err := k.deps.Cluster.Delete(ctx, obj.Ref)
			if err != nil {
				k.journalOpFailure(ctx, attachment, "delete:volumes", "Remove volumes", volumes, err)
				return 0, err
			}
			if removed {
				volumes = append(volumes, "deleted "+obj.Ref.String())
			}
		}
		if len(volumes) > 0 {
			attachment.ensure(ctx)
			attachment.completeStep(ctx, "delete:volumes", "Remove volumes", journal.StepSucceeded, volumes)
		}

		if namespace != nil {
			removed, err := k.deps.Cluster.Delete(ctx, namespace.Ref)
			if err != nil {
				k.journalOpFailure(ctx, attachment, "delete:environment", "Remove environment resources", envDeleted, err)
				return 0, err
			}
			if removed {
				envDeleted = append(envDeleted, "deleted "+namespace.Ref.String())
			}
		}
	}
	if len(envDeleted) > 0 {
		attachment.ensure(ctx)
		attachment.completeStep(ctx, "delete:environment", "Remove environment resources",
			journal.StepSucceeded, envDeleted)
	}

	// Deletion is asynchronous (cascading pods, namespace finalizers); the
	// watch delete events re-enqueue until the snapshot settles, with the
	// requeue as the safety net.
	return requeueHealthCheck, nil
}

// teardownClaims is the seam for service-specific claim teardown (databases,
// buckets). Nothing renders cluster state for claims yet, so releasing them
// is a no-op until the R5/R6 drivers arrive.
func (k *Kernel) teardownClaims(ctx context.Context, environmentID uuid.UUID) {
	_ = ctx
	_ = environmentID
}

// teardownSettled reports whether the destructive decision has been fully
// executed on the cluster: for down no runtime object remains, for
// releasing nothing remains at all.
func teardownSettled(snapshot observe.Snapshot, releasing bool) bool {
	if releasing {
		return len(snapshot.Objects) == 0
	}
	runtime := map[string]bool{
		module.KindAutoscaler: true,
		module.KindWorkload:   true,
		module.KindService:    true,
		module.KindIngress:    true,
		module.KindPod:        true,
	}
	for _, obj := range snapshot.Objects {
		if runtime[obj.Kind] {
			return false
		}
	}
	return true
}

// teardownServices lists the services with observed runtime objects, sorted
// for deterministic journal order.
func teardownServices(snapshot observe.Snapshot) []string {
	seen := map[string]bool{}
	for _, obj := range snapshot.Objects {
		for _, kind := range workloadKinds {
			if obj.Kind == kind && obj.Service != "" {
				seen[obj.Service] = true
			}
		}
	}
	services := make([]string, 0, len(seen))
	for service := range seen {
		services = append(services, service)
	}
	sort.Strings(services)
	return services
}

// observedNamespace finds the environment's namespace in the snapshot.
func observedNamespace(snapshot observe.Snapshot) *observe.Object {
	for index := range snapshot.Objects {
		ref := snapshot.Objects[index].Ref
		if ref.GVK.Group == "" && ref.GVK.Kind == "Namespace" {
			return &snapshot.Objects[index]
		}
	}
	return nil
}
