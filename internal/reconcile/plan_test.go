package reconcile

import (
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/manifest"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/observe"
)

func planDeployment(replicas *int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Name: "demo-web", Namespace: "ns"},
		Spec:       appsv1.DeploymentSpec{Replicas: replicas},
	}
}

func planAutoscaler() *autoscalingv2.HorizontalPodAutoscaler {
	return &autoscalingv2.HorizontalPodAutoscaler{
		TypeMeta:   metav1.TypeMeta{APIVersion: "autoscaling/v2", Kind: "HorizontalPodAutoscaler"},
		ObjectMeta: metav1.ObjectMeta{Name: "demo-web", Namespace: "ns"},
	}
}

func ownedWorkload(t *testing.T, owned bool) *observe.Object {
	t.Helper()
	fields := &metav1.FieldsV1{}
	if owned {
		fields.SetRawBytes([]byte(`{"f:spec": {"f:replicas": {}, "f:selector": {}}}`))
	} else {
		fields.SetRawBytes([]byte(`{"f:spec": {"f:selector": {}}}`))
	}
	return &observe.Object{
		Ref: kube.ObjectRef{
			GVK:       schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"},
			Namespace: "ns", Name: "demo-web",
		},
		Kind: module.KindWorkload,
		ManagedFields: []metav1.ManagedFieldsEntry{{
			Manager:   kube.FieldManagerProject,
			Operation: metav1.ManagedFieldsOperationApply,
			FieldsV1:  fields,
		}},
	}
}

// Fixed to autoscaled: the exact transition order is the contract; a wrong
// order transiently resets the replica count through the default.
func TestPlanScaleOpsFixedToAutoscaled(t *testing.T) {
	t.Parallel()
	objs := serviceObjects{deployment: planDeployment(nil), autoscaler: planAutoscaler()}
	ops := planServiceOps(objs, ownedWorkload(t, true), nil)
	require.Len(t, ops, 3)
	require.Equal(t, OpApply, ops[0].Kind)
	require.Equal(t, "HorizontalPodAutoscaler", ops[0].Object.GetObjectKind().GroupVersionKind().Kind)
	require.Equal(t, OpDisown, ops[1].Kind)
	require.Equal(t, []string{kube.ReplicasFieldPath}, ops[1].Paths)
	require.Equal(t, OpApply, ops[2].Kind)
	require.Equal(t, "Deployment", ops[2].Object.GetObjectKind().GroupVersionKind().Kind)
	require.False(t, ops[2].Force)

	// Steady state autoscaled (ownership already released): no disown.
	ops = planServiceOps(objs, ownedWorkload(t, false), nil)
	require.Len(t, ops, 2)
	require.Equal(t, OpApply, ops[0].Kind)
	require.Equal(t, OpApply, ops[1].Kind)
}

// Autoscaled to fixed: delete the HPA before retaking replicas with one
// forced apply carrying an explicit value.
func TestPlanScaleOpsAutoscaledToFixed(t *testing.T) {
	t.Parallel()
	replicas := int32(2)
	objs := serviceObjects{deployment: planDeployment(&replicas)}
	liveHPA := &observe.Object{
		Ref: kube.ObjectRef{
			GVK:       schema.GroupVersionKind{Group: "autoscaling", Version: "v2", Kind: "HorizontalPodAutoscaler"},
			Namespace: "ns", Name: "demo-web",
		},
		Kind: module.KindAutoscaler,
	}
	ops := planServiceOps(objs, ownedWorkload(t, false), liveHPA)
	require.Len(t, ops, 2)
	require.Equal(t, OpDelete, ops[0].Kind)
	require.Equal(t, liveHPA.Ref, ops[0].Ref)
	require.Equal(t, OpApply, ops[1].Kind)
	require.True(t, ops[1].Force, "retaking replicas ownership requires one forced apply")

	// Steady state fixed (we own replicas, no HPA): a plain apply.
	ops = planServiceOps(objs, ownedWorkload(t, true), nil)
	require.Len(t, ops, 1)
	require.Equal(t, OpApply, ops[0].Kind)
	require.False(t, ops[0].Force)

	// First creation: nothing live, plain apply.
	ops = planServiceOps(objs, nil, nil)
	require.Len(t, ops, 1)
	require.False(t, ops[0].Force)
}

func TestPlanPruneNeverTouchesStatefulKinds(t *testing.T) {
	t.Parallel()
	observed := []observe.Object{
		{Ref: kube.ObjectRef{GVK: schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, Namespace: "ns", Name: "stale"}},
		{Ref: kube.ObjectRef{GVK: schema.GroupVersionKind{Version: "v1", Kind: "PersistentVolumeClaim"}, Namespace: "ns", Name: "data"}},
		{Ref: kube.ObjectRef{GVK: schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}, Name: "ns"}},
		{Ref: kube.ObjectRef{GVK: schema.GroupVersionKind{Group: "autoscaling", Version: "v2", Kind: "HorizontalPodAutoscaler"}, Namespace: "ns", Name: "stale"}},
		{Ref: kube.ObjectRef{GVK: schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, Namespace: "ns", Name: "kept"}},
	}
	desired := []kube.ObjectRef{
		{GVK: schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, Namespace: "ns", Name: "kept"},
	}
	prune := planPrune(observed, desired)
	require.Len(t, prune, 2)
	for _, ref := range prune {
		require.NotEqual(t, "PersistentVolumeClaim", ref.GVK.Kind)
		require.NotEqual(t, "Namespace", ref.GVK.Kind)
		require.NotEqual(t, "kept", ref.Name)
	}
}

func TestPlanBatchesWaitsOnUnsupportedKinds(t *testing.T) {
	t.Parallel()
	document, err := manifest.Parse([]byte(`version: "1"
name: demo
applications:
  api:
    image: ghcr.io/example/api:1.0.0
    environment:
      DATABASE_URL: "{{ databases.main.url }}"
  worker:
    image: ghcr.io/example/worker:1.0.0
databases:
  main:
    engine: postgres
    version: 17
`), "skali.yml")
	require.NoError(t, err)
	result, err := compiler.Compile(document)
	require.NoError(t, err)

	batches, waiting, err := planBatches(result.Definition)
	require.NoError(t, err)
	require.Equal(t, [][]string{{"api", "worker"}}, batches)
	require.Contains(t, waiting, "api")
	require.Contains(t, waiting["api"], "databases.main")
	require.NotContains(t, waiting, "worker")
}

func TestGroupObjectsSplitsByService(t *testing.T) {
	t.Parallel()
	deployment := planDeployment(nil)
	deployment.Labels = map[string]string{"skali.dev/service": "web"}
	service := &corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{Name: "demo-web", Namespace: "ns", Labels: map[string]string{"skali.dev/service": "web"}},
	}
	grouped, refs, err := groupObjects([]runtime.Object{deployment, service})
	require.NoError(t, err)
	require.Len(t, refs, 2)
	require.NotNil(t, grouped["web"].deployment)
	require.Len(t, grouped["web"].rest, 1)
}
