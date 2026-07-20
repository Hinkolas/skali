package kube

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// Recorded from a real server-side apply of a Deployment: the Apply entry of
// skalid-project owning replicas plus template fields, and an Update entry
// of the deployment controller owning status.
func fixtureEntries(t *testing.T) []metav1.ManagedFieldsEntry {
	t.Helper()
	applied := &metav1.FieldsV1{}
	applied.SetRawBytes([]byte(`{
		"f:metadata": {"f:labels": {"f:skali.dev/managed": {}}},
		"f:spec": {
			"f:replicas": {},
			"f:selector": {},
			"f:template": {"f:metadata": {"f:labels": {"f:app": {}}}}
		}
	}`))
	status := &metav1.FieldsV1{}
	status.SetRawBytes([]byte(`{"f:status": {"f:readyReplicas": {}}}`))
	return []metav1.ManagedFieldsEntry{
		{Manager: FieldManagerProject, Operation: metav1.ManagedFieldsOperationApply, FieldsV1: applied},
		{Manager: "kube-controller-manager", Operation: metav1.ManagedFieldsOperationUpdate, FieldsV1: status},
	}
}

func TestOwnsField(t *testing.T) {
	t.Parallel()
	entries := fixtureEntries(t)
	require.True(t, OwnsField(entries, FieldManagerProject, ReplicasFieldPath))
	require.True(t, OwnsField(entries, FieldManagerProject, "f:spec.f:template.f:metadata"))
	require.False(t, OwnsField(entries, FieldManagerProject, "f:spec.f:strategy"))
	// Update entries never count as apply ownership.
	require.False(t, OwnsField(entries, "kube-controller-manager", "f:status.f:readyReplicas"))
	require.False(t, OwnsField(entries, "someone-else", ReplicasFieldPath))
}

func TestRemoveOwnedFields(t *testing.T) {
	t.Parallel()
	entries := fixtureEntries(t)
	rewritten, changed, err := RemoveOwnedFields(entries, FieldManagerProject, ReplicasFieldPath)
	require.NoError(t, err)
	require.True(t, changed)
	require.False(t, OwnsField(rewritten, FieldManagerProject, ReplicasFieldPath))
	require.True(t, OwnsField(rewritten, FieldManagerProject, "f:spec.f:selector"),
		"unrelated ownership must survive the rewrite")
	// The original entries are untouched.
	require.True(t, OwnsField(entries, FieldManagerProject, ReplicasFieldPath))

	// Removing again is a no-op.
	_, changed, err = RemoveOwnedFields(rewritten, FieldManagerProject, ReplicasFieldPath)
	require.NoError(t, err)
	require.False(t, changed)
}

// Releasing the last child must prune the emptied ancestor: leaving
// "f:spec": {} behind would flip the entry from owning one child to owning
// spec itself.
func TestRemoveOwnedFieldsPrunesEmptyAncestors(t *testing.T) {
	t.Parallel()
	only := &metav1.FieldsV1{}
	only.SetRawBytes([]byte(`{"f:spec": {"f:replicas": {}}}`))
	entries := []metav1.ManagedFieldsEntry{{
		Manager:   FieldManagerProject,
		Operation: metav1.ManagedFieldsOperationApply,
		FieldsV1:  only,
	}}
	rewritten, changed, err := RemoveOwnedFields(entries, FieldManagerProject, ReplicasFieldPath)
	require.NoError(t, err)
	require.True(t, changed)
	fields := map[string]any{}
	require.NoError(t, json.Unmarshal(rewritten[0].FieldsV1.GetRawBytes(), &fields))
	require.NotContains(t, fields, "f:spec")
}

func TestStripServerFields(t *testing.T) {
	t.Parallel()
	replicas := int32(2)
	deployment := &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "demo", Image: "example.invalid/demo:1"}}},
			},
		},
	}
	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(deployment)
	require.NoError(t, err)
	applied := &unstructured.Unstructured{Object: content}
	StripServerFields(applied)

	_, present, err := unstructured.NestedMap(applied.Object, "status")
	require.NoError(t, err)
	require.False(t, present, "status must never be part of an applied configuration")
	raw, err := json.Marshal(applied.Object)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "creationTimestamp",
		"null creationTimestamp fields must be stripped recursively")
}
