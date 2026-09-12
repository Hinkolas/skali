package bundle

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	kt "k8s.io/client-go/testing"

	"github.com/Hinkolas/skali/internal/kube"
)

// An upgrade renders skali-bootstrap-user with the new skalid image; the
// API server refuses the changed pod template on the finished job from the
// previous release. The applier must delete that job and create the new
// one instead of failing the converge (issue 23).
func TestApplyObjectsReplacesJobWithImmutableTemplate(t *testing.T) {
	t.Parallel()
	objects, err := Render(Profile{
		SkalidImage: "ghcr.io/hinkolas/skalid:v0.1.0-alpha.8", AuthSecret: "secret",
		AdminEmail: "admin@example.com", AdminPassword: "password", RegistryHost: "localhost:5510",
	})
	require.NoError(t, err)
	var job *unstructured.Unstructured
	for index := range objects.BootstrapUser {
		if objects.BootstrapUser[index].GetKind() == "Job" {
			job = &objects.BootstrapUser[index]
		}
	}
	require.NotNil(t, job)

	previous := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: job.GetName(), Namespace: job.GetNamespace()}}
	clientset := k8sfake.NewSimpleClientset(previous)
	jobKind := schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}
	dynamic := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{{Group: "batch", Version: "v1"}})
	mapper.Add(jobKind, meta.RESTScopeNamespace)

	// First apply: the finished alpha.6 job is live and the template
	// differs. Second apply, after the delete: creation succeeds.
	applies, deleted := 0, false
	dynamic.PrependReactor("patch", "jobs", func(action kt.Action) (bool, runtime.Object, error) {
		applies++
		if !deleted {
			return true, nil, apierrors.NewInvalid(jobKind.GroupKind(), job.GetName(),
				field.ErrorList{field.Invalid(field.NewPath("spec", "template"), "{...}", "field is immutable")})
		}
		return true, job.DeepCopy(), nil
	})
	clientset.PrependReactor("delete", "jobs", func(action kt.Action) (bool, runtime.Object, error) {
		deleted = true
		return false, nil, nil
	})

	applier := &Applier{Client: &kube.Client{Clientset: clientset, Dynamic: dynamic, Mapper: mapper},
		PollInterval: time.Millisecond, WaitTimeout: time.Second}
	require.NoError(t, applier.ApplyObjects(context.Background(), []unstructured.Unstructured{*job}))
	require.Equal(t, 2, applies, "the apply is retried once after the delete")
	require.True(t, deleted)
	_, err = clientset.BatchV1().Jobs(job.GetNamespace()).Get(context.Background(), job.GetName(), metav1.GetOptions{})
	require.True(t, apierrors.IsNotFound(err), "the previous job is gone")
}

// Any other invalid apply, and an immutable-field rejection on a kind whose
// replacement is not idempotent, still fail the converge.
func TestApplyObjectsDoesNotReplaceOtherInvalidObjects(t *testing.T) {
	t.Parallel()
	deployment := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "skalid", "namespace": Namespace},
	}}
	kind := schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
	dynamic := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{{Group: "apps", Version: "v1"}})
	mapper.Add(kind, meta.RESTScopeNamespace)
	dynamic.PrependReactor("patch", "deployments", func(action kt.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewInvalid(kind.GroupKind(), "skalid",
			field.ErrorList{field.Invalid(field.NewPath("spec", "selector"), "{...}", "field is immutable")})
	})
	applier := &Applier{Client: &kube.Client{Clientset: k8sfake.NewSimpleClientset(), Dynamic: dynamic, Mapper: mapper},
		PollInterval: time.Millisecond, WaitTimeout: time.Second}
	err := applier.ApplyObjects(context.Background(), []unstructured.Unstructured{*deployment})
	require.ErrorContains(t, err, "field is immutable")
}
