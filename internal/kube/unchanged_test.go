package kube

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/managedfields"
	kt "k8s.io/client-go/testing"
)

// appliedSecret is an applied configuration: a values Secret with data.
func appliedSecret(owner string, data map[string]any) *unstructured.Unstructured {
	applied := ownedSecret(owner)
	applied.Object["data"] = data
	StripServerFields(applied)
	return applied
}

// liveAfter is the object a server holds after manager applied config at
// apiVersion: the configuration plus server fields, with the manager owning
// exactly the configuration's fields.
func liveAfter(t *testing.T, config *unstructured.Unstructured, manager, apiVersion string) *unstructured.Unstructured {
	t.Helper()
	typedConfig, err := managedfields.NewDeducedTypeConverter().ObjectToTyped(config)
	require.NoError(t, err)
	set, err := typedConfig.ToFieldSet()
	require.NoError(t, err)
	raw, err := set.Difference(strippedFields).ToJSON()
	require.NoError(t, err)
	live := config.DeepCopy()
	live.SetUID("first")
	live.SetResourceVersion("7")
	live.SetManagedFields([]metav1.ManagedFieldsEntry{{
		Manager: manager, Operation: metav1.ManagedFieldsOperationApply, APIVersion: apiVersion,
		FieldsType: "FieldsV1", FieldsV1: &metav1.FieldsV1{Raw: raw},
	}})
	return live
}

// An apply is a no-op only when the manager owns exactly the
// configuration's fields at its version and merging it changes no value.
func TestUnchangedBy(t *testing.T) {
	converter := managedfields.NewDeducedTypeConverter()
	owner := uuid.NewString()
	applied := appliedSecret(owner, map[string]any{"KEY": "dmFsdWU="})

	live := liveAfter(t, applied, FieldManagerProject, "v1")
	require.True(t, unchangedBy(converter, applied, live, FieldManagerProject))

	other := live.DeepCopy()
	other.SetAnnotations(map[string]string{identityAnnotation: applied.GetAnnotations()[identityAnnotation], "example.com/other": "x"})
	require.True(t, unchangedBy(converter, applied, other, FieldManagerProject),
		"another manager's field is not the configuration's concern")

	changed := appliedSecret(owner, map[string]any{"KEY": "Y2hhbmdlZA=="})
	require.False(t, unchangedBy(converter, changed, live, FieldManagerProject), "a changed value")

	wider := liveAfter(t, appliedSecret(owner, map[string]any{"KEY": "dmFsdWU=", "OLD": "b2xk"}), FieldManagerProject, "v1")
	require.False(t, unchangedBy(converter, applied, wider, FieldManagerProject),
		"a dropped field the server would prune")

	require.False(t, unchangedBy(converter, applied, liveAfter(t, applied, FieldManagerProject, "v2"), FieldManagerProject),
		"ownership recorded at another version")
	require.False(t, unchangedBy(converter, applied, liveAfter(t, applied, "other", "v1"), FieldManagerProject),
		"a manager that owns nothing")
}

// Apply ends at its read when the apply would change nothing, unless it is
// forced; without a schema it always applies.
func TestApplySkipsUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name   string
		schema bool
		force  bool
		sent   bool
	}{
		{"unchanged", true, false, false},
		{"forced", true, true, true},
		{"no schema", false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := uuid.NewString()
			applied := appliedSecret(owner, map[string]any{"KEY": "dmFsdWU="})
			client, dynamic := ownershipClient(liveAfter(t, applied, FieldManagerProject, "v1"))
			if tc.schema {
				client.schemas.entries = map[schema.GroupVersion]schemaEntry{
					{Version: "v1"}: {converter: managedfields.NewDeducedTypeConverter(), checked: time.Now()},
				}
			}
			patches := 0
			dynamic.PrependReactor("patch", "secrets", func(kt.Action) (bool, runtime.Object, error) {
				patches++
				return true, liveAfter(t, applied, FieldManagerProject, "v1"), nil
			})
			result, err := client.Apply(context.Background(), applied, tc.force)
			require.NoError(t, err)
			require.False(t, result.Changed)
			require.Equal(t, tc.sent, patches == 1)
		})
	}
}
