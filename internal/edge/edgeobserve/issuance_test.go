package edgeobserve

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/Hinkolas/skali/internal/edge"
	"github.com/Hinkolas/skali/internal/kube"
)

func failedCertificate() *unstructured.Unstructured {
	obj := certificateObject(map[string]any{
		"failedIssuanceAttempts": int64(2), "lastFailureTime": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
		"conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": "Failed"}, map[string]any{"type": "Issuing", "status": "False", "reason": "Failed"}},
	})
	obj.SetUID("cert-uid")
	obj.SetResourceVersion("7")
	obj.SetGeneration(2)
	return obj
}
func TestRetryFailedCertificate(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		mutate func(*unstructured.Unstructured)
		uid    types.UID
		want   bool
	}{
		{"failed", func(*unstructured.Unstructured) {}, "cert-uid", true},
		{"replacement", func(*unstructured.Unstructured) {}, "old-uid", false},
		{"no identity", func(*unstructured.Unstructured) {}, "", false},
		{"valid renewal", func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "status", "notAfter")
		}, "cert-uid", false},
		{"in flight", func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedSlice(o.Object, []any{map[string]any{"type": "Issuing", "status": "True"}}, "status", "conditions")
		}, "cert-uid", false},
		{"failure in current promotion", func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, time.Now().UTC().Format(time.RFC3339), "status", "lastFailureTime")
		}, "cert-uid", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := failedCertificate()
			tc.mutate(obj)
			client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), obj)
			ref := kube.ObjectRef{GVK: edge.CertificateGVK, Namespace: obj.GetNamespace(), Name: obj.GetName(), UID: tc.uid}
			changed, err := RetryFailedCertificate(ctx, client, ref, time.Now())
			require.NoError(t, err)
			require.Equal(t, tc.want, changed)
			// A fresh client call represents another reconciliation, or a daemon
			// restart: it must not issue again while the request is in flight.
			changed, err = RetryFailedCertificate(ctx, client, ref, time.Now())
			require.NoError(t, err)
			require.False(t, changed)
			if tc.want {
				live, err := client.Resource(edge.CertificateGVR).Namespace(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
				require.NoError(t, err)
				projected, _ := ConvertCertificate(live)
				require.True(t, projected.Certificate.Issuing)
				require.Equal(t, int32(2), projected.Certificate.FailedAttempts)
				require.True(t, projected.Certificate.NextRetryTime.IsZero())
				require.Equal(t, "ManuallyTriggered", projected.Certificate.Reason)
			}
		})
	}
}
func TestRetryCertificateConflictAndErrors(t *testing.T) {
	obj := failedCertificate()
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), obj)
	count := 0
	client.PrependReactor("update", "certificates", func(action ktesting.Action) (bool, runtime.Object, error) {
		require.Equal(t, "status", action.GetSubresource())
		count++
		if count == 1 {
			return true, nil, apierrors.NewConflict(edge.CertificateGVR.GroupResource(), obj.GetName(), nil)
		}
		return false, nil, nil
	})
	changed, err := RetryFailedCertificate(context.Background(), client, kube.ObjectRef{Namespace: obj.GetNamespace(), Name: obj.GetName(), UID: obj.GetUID()}, time.Now())
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, 2, count)
}
func TestInspectCertificateUsesOwnerAndRevision(t *testing.T) {
	cert := failedCertificate()
	makeChild := func(api, kind, name string, parent *unstructured.Unstructured) *unstructured.Unstructured {
		o := &unstructured.Unstructured{Object: map[string]any{"apiVersion": api, "kind": kind, "metadata": map[string]any{"name": name, "namespace": cert.GetNamespace()}, "status": map[string]any{"state": "invalid", "reason": "DNS points at the wrong address"}}}
		o.SetUID(types.UID(name))
		controller := true
		o.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: parent.GetAPIVersion(), Kind: parent.GetKind(), Name: parent.GetName(), UID: parent.GetUID(), Controller: &controller}})
		return o
	}
	request := makeChild("cert-manager.io/v1", "CertificateRequest", "current-request", cert)
	request.SetAnnotations(map[string]string{"cert-manager.io/certificate-revision": "1"})
	old := makeChild("cert-manager.io/v1", "CertificateRequest", "old-request", cert)
	old.SetAnnotations(map[string]string{"cert-manager.io/certificate-revision": "0"})
	order := makeChild("acme.cert-manager.io/v1", "Order", "current-order", request)
	challenge := makeChild("acme.cert-manager.io/v1", "Challenge", "challenge", order)
	_ = unstructured.SetNestedField(challenge.Object, "example.com", "spec", "dnsName")
	fakeOrder := makeChild("acme.cert-manager.io/v1", "Order", "unrelated-order", old)
	lists := map[schema.GroupVersionResource]string{
		{Group: "cert-manager.io", Version: "v1", Resource: "certificaterequests"}: "CertificateRequestList",
		{Group: "acme.cert-manager.io", Version: "v1", Resource: "orders"}:         "OrderList",
		{Group: "acme.cert-manager.io", Version: "v1", Resource: "challenges"}:     "ChallengeList",
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), lists, cert, request, old, order, challenge, fakeOrder)
	status, details, err := InspectCertificate(context.Background(), client, kube.ObjectRef{Namespace: cert.GetNamespace(), Name: cert.GetName(), UID: cert.GetUID()})
	require.NoError(t, err)
	require.Equal(t, int32(2), status.FailedAttempts)
	require.Equal(t, "current-request", details["request"])
	require.Equal(t, "current-order", details["order"])
	require.Equal(t, "invalid", details["order_state"])
	require.Contains(t, details["challenges"], "example.com")
	require.Contains(t, details["challenges"], "DNS points at the wrong address")
	client.PrependReactor("list", "orders", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "acme.cert-manager.io", Resource: "orders"}, "", nil)
	})
	status, details, err = InspectCertificate(context.Background(), client, kube.ObjectRef{Namespace: cert.GetNamespace(), Name: cert.GetName(), UID: cert.GetUID()})
	require.ErrorContains(t, err, "orders")
	require.NotNil(t, status)
	require.Equal(t, "current-request", details["request"])
}
