package kube

import (
	"context"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	kt "k8s.io/client-go/testing"
	"testing"
)

var secretKind = schema.GroupVersionKind{Version: "v1", Kind: "Secret"}

func ownedSecret(owner string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "values", "namespace": "skali-" + owner}}}
	obj.SetLabels(map[string]string{"skali.dev/managed": "true", "skali.dev/environment": owner})
	stampIdentity(obj)
	obj.SetUID(types.UID("first"))
	obj.SetResourceVersion("7")
	return obj
}
func ownershipClient(objects ...runtime.Object) (*Client, *fake.FakeDynamicClient) {
	dynamic := fake.NewSimpleDynamicClient(runtime.NewScheme(), objects...)
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{{Version: "v1"}})
	mapper.Add(secretKind, meta.RESTScopeNamespace)
	return &Client{Dynamic: dynamic, Mapper: mapper}, dynamic
}

func TestOwnershipRejectsForeignAndUnownedEvenWithForce(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		owner := uuid.NewString()
		want := ownedSecret(owner)
		live := want.DeepCopy()
		if foreign {
			live.SetLabels(map[string]string{"skali.dev/managed": "true", "skali.dev/environment": uuid.NewString()})
		} else {
			live.SetAnnotations(nil)
		}
		client, _ := ownershipClient(live)
		_, err := client.Apply(context.Background(), want, true)
		require.ErrorContains(t, err, "foreign or unowned")
		ref := ObjectRef{GVK: secretKind, Namespace: want.GetNamespace(), Name: want.GetName()}
		_, err = client.Delete(context.Background(), ref)
		require.ErrorContains(t, err, "foreign or unowned")
		err = client.DisownFields(context.Background(), ref, FieldManagerProject, "f:data")
		require.ErrorContains(t, err, "foreign or unowned")
	}
}

func TestOwnershipCreationRaceAndDeletePreconditions(t *testing.T) {
	owner := uuid.NewString()
	want := ownedSecret(owner)
	client, dynamic := ownershipClient()
	dynamic.PrependReactor("create", "secrets", func(action kt.Action) (bool, runtime.Object, error) {
		other := want.DeepCopy()
		other.SetAnnotations(nil)
		require.NoError(t, dynamic.Tracker().Create(schema.GroupVersionResource{Version: "v1", Resource: "secrets"}, other, want.GetNamespace()))
		return true, nil, apierrors.NewAlreadyExists(schema.GroupResource{Resource: "secrets"}, want.GetName())
	})
	_, err := client.Apply(context.Background(), want, false)
	require.ErrorContains(t, err, "foreign or unowned")
	client, dynamic = ownershipClient(want)
	dynamic.PrependReactor("delete", "secrets", func(action kt.Action) (bool, runtime.Object, error) {
		options := action.(kt.DeleteAction).GetDeleteOptions()
		require.Equal(t, &metav1.Preconditions{UID: new(types.UID("first")), ResourceVersion: new("7")}, options.Preconditions)
		return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, want.GetName(), nil)
	})
	_, err = client.Delete(context.Background(), ObjectRef{GVK: secretKind, Namespace: want.GetNamespace(), Name: want.GetName(), UID: want.GetUID()})
	require.Error(t, err)
}
