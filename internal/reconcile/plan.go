package reconcile

import (
	"fmt"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/kube"
	rendering "github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/observe"
)

// OpKind is one primitive cluster operation.
type OpKind string

const (
	OpApply  OpKind = "apply"
	OpDelete OpKind = "delete"
	OpDisown OpKind = "disown"
)

// Op is one planned cluster operation. Ops are ordered: scale-mode
// transitions depend on it (autoscaler before releasing replicas; deleting
// the autoscaler before retaking them).
type Op struct {
	Kind   OpKind
	Object runtime.Object // OpApply
	Ref    kube.ObjectRef // OpDelete, OpDisown
	Force  bool           // OpApply: retake field ownership with explicit values
	Paths  []string       // OpDisown
}

// serviceObjects groups one service's rendered objects by role.
type serviceObjects struct {
	pvcs       []runtime.Object
	deployment *appsv1.Deployment
	autoscaler *autoscalingv2.HorizontalPodAutoscaler
	rest       []runtime.Object // services, ingresses
}

// planServiceOps orders one service's operations around the scale-mode
// contract. The desired mode comes exclusively from the revision (an
// autoscaler is rendered or not); observation only informs the transition
// mechanics, so a stale cache can never flip modes.
//
// Fixed to autoscaled: apply the HPA first, release f:spec.f:replicas from
// skalid's apply entry, then apply the Deployment without replicas; the
// live count persists. Autoscaled to fixed: delete the HPA so its
// controller stops writing, then one forced apply WITH replicas retakes
// ownership through an explicit value, never through the default.
func planServiceOps(objs serviceObjects, liveWorkload, liveAutoscaler *observe.Object) []Op {
	ops := make([]Op, 0, 4+len(objs.pvcs)+len(objs.rest))
	for _, pvc := range objs.pvcs {
		ops = append(ops, Op{Kind: OpApply, Object: pvc})
	}
	switch {
	case objs.autoscaler != nil:
		ops = append(ops, Op{Kind: OpApply, Object: objs.autoscaler})
		if liveWorkload != nil && kube.OwnsField(liveWorkload.ManagedFields, kube.FieldManagerProject, kube.ReplicasFieldPath) {
			ops = append(ops, Op{Kind: OpDisown, Ref: liveWorkload.Ref, Paths: []string{kube.ReplicasFieldPath}})
		}
		if objs.deployment != nil {
			ops = append(ops, Op{Kind: OpApply, Object: objs.deployment})
		}
	default:
		if liveAutoscaler != nil {
			ops = append(ops, Op{Kind: OpDelete, Ref: liveAutoscaler.Ref})
		}
		if objs.deployment != nil {
			force := liveWorkload != nil &&
				!kube.OwnsField(liveWorkload.ManagedFields, kube.FieldManagerProject, kube.ReplicasFieldPath)
			ops = append(ops, Op{Kind: OpApply, Object: objs.deployment, Force: force})
		}
	}
	ops = append(ops, applyAll(objs.rest)...)
	return ops
}

func applyAll(objects []runtime.Object) []Op {
	ops := make([]Op, 0, len(objects))
	for _, obj := range objects {
		ops = append(ops, Op{Kind: OpApply, Object: obj})
	}
	return ops
}

// prunableKinds are the stateless kinds the reconciler may delete when they
// are owned by the environment and absent from the desired set. Namespaces
// and PersistentVolumeClaims are never pruned: stateful removal requires an
// explicit destructive transition (a later milestone). Secrets are not
// observed in R2 (the one rendered Secret is always desired), so they are
// not prunable either.
var prunableKinds = map[schema.GroupKind]bool{
	{Group: "apps", Kind: "Deployment"}:                     true,
	{Group: "", Kind: "Service"}:                            true,
	{Group: "networking.k8s.io", Kind: "Ingress"}:           true,
	{Group: "autoscaling", Kind: "HorizontalPodAutoscaler"}: true,
}

// planPrune lists observed objects of the environment that are prunable and
// absent from the desired set, with their UIDs as delete preconditions.
// Callers must skip pruning entirely when the desired set could not be
// built: absence caused by a compiler error never prunes anything.
func planPrune(observed []observe.Object, desired []kube.ObjectRef) []kube.ObjectRef {
	type refKey struct {
		group, kind, namespace, name string
	}
	want := make(map[refKey]bool, len(desired))
	for _, ref := range desired {
		want[refKey{ref.GVK.Group, ref.GVK.Kind, ref.Namespace, ref.Name}] = true
	}
	var prune []kube.ObjectRef
	for _, obj := range observed {
		gk := schema.GroupKind{Group: obj.Ref.GVK.Group, Kind: obj.Ref.GVK.Kind}
		if !prunableKinds[gk] {
			continue
		}
		if want[refKey{obj.Ref.GVK.Group, obj.Ref.GVK.Kind, obj.Ref.Namespace, obj.Ref.Name}] {
			continue
		}
		prune = append(prune, obj.Ref)
	}
	sort.Slice(prune, func(i, j int) bool { return prune[i].String() < prune[j].String() })
	return prune
}

// planBatches orders the revision's reconcilable services (applications and
// databases) into dependency batches of dotted names. Dependencies on
// service kinds the reconciler cannot manage yet (buckets, until R6) put
// the dependent service into the waiting map with a reason instead of
// failing the whole plan.
func planBatches(definition compiler.ProjectDefinition) (batches [][]string, waiting map[string]string, err error) {
	services := make([]string, 0, len(definition.Applications)+len(definition.Databases))
	for key := range definition.Applications {
		services = append(services, "applications."+key)
	}
	for key := range definition.Databases {
		services = append(services, "databases."+key)
	}
	sort.Strings(services)
	included := make(map[string]bool, len(services))
	for _, dotted := range services {
		included[dotted] = true
	}

	waiting = make(map[string]string)
	dependencies := make(map[string][]string, len(services))
	for _, dotted := range services {
		for _, dependency := range definition.Dependencies[dotted] {
			if included[dependency] {
				dependencies[dotted] = append(dependencies[dotted], dependency)
				continue
			}
			waiting[dotted] = "waiting for " + dependency + ": service kind is not reconciled yet"
		}
	}
	ordered, err := module.Order(services, dependencies)
	if err != nil {
		return nil, nil, fmt.Errorf("reconcile: order services: %w", err)
	}
	for i := range ordered {
		sort.Strings(ordered[i])
	}
	return ordered, waiting, nil
}

// splitService separates a dotted service name into its collection and bare
// key.
func splitService(dotted string) (collection, key string) {
	if index := strings.IndexByte(dotted, '.'); index >= 0 {
		return dotted[:index], dotted[index+1:]
	}
	return "", dotted
}

// desiredSet is one revision's rendered desired state.
type desiredSet struct {
	namespace *corev1.Namespace
	secret    *corev1.Secret
	services  map[string]serviceObjects
	refs      []kube.ObjectRef // every desired object, for prune planning
}

// groupObjects splits the flat rendered object list per service key.
func groupObjects(objects []runtime.Object) (map[string]serviceObjects, []kube.ObjectRef, error) {
	services := make(map[string]serviceObjects)
	refs := make([]kube.ObjectRef, 0, len(objects))
	for _, obj := range objects {
		accessor, err := meta.Accessor(obj)
		if err != nil {
			return nil, nil, fmt.Errorf("reconcile: object metadata: %w", err)
		}
		refs = append(refs, kube.ObjectRef{
			GVK:       obj.GetObjectKind().GroupVersionKind(),
			Namespace: accessor.GetNamespace(),
			Name:      accessor.GetName(),
		})
		key := accessor.GetLabels()[rendering.LabelService]
		grouped := services[key]
		switch typed := obj.(type) {
		case *appsv1.Deployment:
			grouped.deployment = typed
		case *autoscalingv2.HorizontalPodAutoscaler:
			grouped.autoscaler = typed
		case *corev1.PersistentVolumeClaim:
			grouped.pvcs = append(grouped.pvcs, typed)
		default:
			grouped.rest = append(grouped.rest, obj)
		}
		services[key] = grouped
	}
	return services, refs, nil
}
