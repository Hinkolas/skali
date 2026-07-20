package observe

import (
	"github.com/google/uuid"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/module"
)

// Fake is the test implementation of the observed store: the identical read
// contract with scripted mutators instead of informers. Reconcile and
// module tests drive it directly.
type Fake struct {
	*Store
}

func NewFake() *Fake {
	return &Fake{Store: NewStore(nil)}
}

// SetFresh marks the fake synced and fresh, the baseline of most tests.
func (f *Fake) SetFresh() {
	f.MarkReady()
}

// SetStale forces the source stale through the real transition path.
func (f *Fake) SetStale() {
	f.MarkReady()
	f.MarkFailure()
	f.EvaluateFreshness(0)
}

// SetWorkload records a deployment-shaped workload for a service. The
// object name is explicit because pruning compares it against the rendered
// desired set.
func (f *Fake) SetWorkload(environmentID uuid.UUID, namespace, objectName, service, revision string, status module.WorkloadStatus) {
	f.Upsert(Object{
		Ref: kube.ObjectRef{
			GVK:       schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"},
			Namespace: namespace,
			Name:      objectName,
		},
		Kind:        module.KindWorkload,
		Name:        service,
		Environment: environmentID,
		Service:     service,
		Revision:    revision,
		Workload:    &status,
	})
}

// SetPod records one pod of a service.
func (f *Fake) SetPod(environmentID uuid.UUID, namespace, service, podName, node string, status module.PodStatus) {
	pod := status
	pod.Node = node
	f.Upsert(Object{
		Ref: kube.ObjectRef{
			GVK:       schema.GroupVersionKind{Version: "v1", Kind: "Pod"},
			Namespace: namespace,
			Name:      podName,
		},
		Kind:        module.KindPod,
		Name:        podName,
		Environment: environmentID,
		Service:     service,
		Node:        node,
		Pod:         &pod,
	})
}

// SetAutoscaler records the autoscaler of a service.
func (f *Fake) SetAutoscaler(environmentID uuid.UUID, namespace, objectName, service string, status module.AutoscalerStatus) {
	f.Upsert(Object{
		Ref: kube.ObjectRef{
			GVK:       schema.GroupVersionKind{Group: "autoscaling", Version: "v2", Kind: "HorizontalPodAutoscaler"},
			Namespace: namespace,
			Name:      objectName,
		},
		Kind:        module.KindAutoscaler,
		Name:        service,
		Environment: environmentID,
		Service:     service,
		Autoscaler:  &status,
	})
}
