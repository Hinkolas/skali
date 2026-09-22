package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"

	"github.com/Hinkolas/skali/internal/kubetest"
)

type ownershipTransport func(*http.Request) (*http.Response, error)

func (f ownershipTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// This test installs only its admission fixture into a disposable cluster. It
// intentionally does not run in parallel with other ownership-policy tests.
func TestLiveOwnershipProtection(t *testing.T) {
	ctx := context.Background()
	config := kubetest.Config(t)
	client, err := NewFromConfig(config)
	require.NoError(t, err)
	for _, ns := range []string{OwnershipProbeNamespace} {
		_, err := client.Clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		if !apierrors.IsAlreadyExists(err) {
			require.NoError(t, err)
		}
	}
	policyAPI := client.Clientset.AdmissionregistrationV1()
	_, err = policyAPI.ValidatingAdmissionPolicies().Create(ctx, OwnershipPolicy(), metav1.CreateOptions{})
	if !apierrors.IsAlreadyExists(err) {
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = policyAPI.ValidatingAdmissionPolicies().Delete(ctx, OwnershipPolicyName, metav1.DeleteOptions{})
		})
	}
	_, err = policyAPI.ValidatingAdmissionPolicyBindings().Create(ctx, OwnershipPolicyBinding(), metav1.CreateOptions{})
	if !apierrors.IsAlreadyExists(err) {
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = policyAPI.ValidatingAdmissionPolicyBindings().Delete(ctx, OwnershipPolicyName, metav1.DeleteOptions{})
		})
	}
	_, err = client.Clientset.CoreV1().ConfigMaps(OwnershipProbeNamespace).Create(ctx, OwnershipProbe(), metav1.CreateOptions{})
	if !apierrors.IsAlreadyExists(err) {
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = client.Clientset.CoreV1().ConfigMaps(OwnershipProbeNamespace).Delete(ctx, OwnershipProbeName, metav1.DeleteOptions{})
		})
	}
	require.EventuallyWithT(t, func(c *assert.CollectT) { assert.NoError(c, client.VerifyOwnershipPolicy(ctx)) }, time.Minute, 500*time.Millisecond)
	owner := uuid.NewString()
	ns := "skali-" + owner
	namespace := &corev1.Namespace{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"}, ObjectMeta: metav1.ObjectMeta{Name: ns, Labels: map[string]string{"skali.dev/managed": "true", "skali.dev/environment": owner}}}
	_, err = client.Apply(ctx, namespace, false)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Clientset.CoreV1().Namespaces().Delete(ctx, ns, metav1.DeleteOptions{}) })

	t.Run("status race", func(t *testing.T) {
		for _, protected := range []bool{false, true} {
			t.Run(fmt.Sprint(protected), func(t *testing.T) {
				hpa := &autoscalingv2.HorizontalPodAutoscaler{TypeMeta: metav1.TypeMeta{APIVersion: "autoscaling/v2", Kind: "HorizontalPodAutoscaler"}, ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("race-%t", protected), Namespace: ns, Labels: namespace.Labels}, Spec: autoscalingv2.HorizontalPodAutoscalerSpec{ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "not-running"}, MinReplicas: new(int32(1)), MaxReplicas: 2}}
				_, err := client.Apply(ctx, hpa, false)
				require.NoError(t, err)
				var calls atomic.Int32
				wrapped := rest.CopyConfig(config)
				if protected {
					// The test cluster's internal endpoint is behind Docker on macOS. Map
					// direct peer dials to that same single server's published localhost port.
					// Production uses the advertised peer addresses without this override.
					endpoint, err := url.Parse(config.Host)
					require.NoError(t, err)
					wrapped.Dial = func(ctx context.Context, network, address string) (net.Conn, error) {
						return (&net.Dialer{}).DialContext(ctx, network, endpoint.Host)
					}
				}
				wrapped.Wrap(func(base http.RoundTripper) http.RoundTripper {
					return ownershipTransport(func(r *http.Request) (*http.Response, error) {
						if r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/horizontalpodautoscalers/"+hpa.Name) {
							if calls.Add(1) == 1 {
								err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
									live, err := client.Clientset.AutoscalingV2().HorizontalPodAutoscalers(ns).Get(ctx, hpa.Name, metav1.GetOptions{})
									if err != nil {
										return err
									}
									live.Status.CurrentReplicas = 123
									_, err = client.Clientset.AutoscalingV2().HorizontalPodAutoscalers(ns).UpdateStatus(ctx, live, metav1.UpdateOptions{})
									return err
								})
								require.NoError(t, err)
							}
						}
						return base.RoundTrip(r)
					})
				})
				applying, err := NewFromConfig(wrapped)
				require.NoError(t, err)
				if protected {
					watchCtx, cancel := context.WithCancel(ctx)
					done := make(chan error, 1)
					go func() { done <- applying.watchOwnershipProtection(watchCtx) }()
					t.Cleanup(func() { cancel(); <-done })
					require.Eventually(t, func() bool { g := applying.ownership.Load(); return g != nil && g.valid.Load() }, 10*time.Second, 10*time.Millisecond, "real policy, membership and probes must enable protection")
				}
				hpa.Spec.MaxReplicas = 3
				result, err := applying.Apply(ctx, hpa, false)
				require.NoError(t, err)
				require.True(t, result.Changed)
				if protected {
					require.EqualValues(t, 1, calls.Load())
				} else {
					require.EqualValues(t, 2, calls.Load())
				}
				second, err := applying.Apply(ctx, hpa, false)
				require.NoError(t, err)
				require.False(t, second.Changed)
			})
		}
	})

	t.Run("ownership metadata", func(t *testing.T) {
		objects := []runtime.Object{&unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "traefik.io/v1alpha1", "kind": "Middleware",
			"metadata": map[string]any{"name": "ownership", "namespace": ns, "labels": map[string]any{"skali.dev/managed": "true", "skali.dev/environment": owner}},
			"spec":     map[string]any{"compress": map[string]any{}},
		}}, namespace, &corev1.ConfigMap{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"}, ObjectMeta: metav1.ObjectMeta{Name: "metadata", Namespace: ns, Labels: namespace.Labels}}}
		for _, obj := range objects {
			applied, err := client.Apply(ctx, obj, false)
			require.NoError(t, err)
			resource, err := client.resource(applied.Live.GroupVersionKind(), applied.Live.GetNamespace())
			require.NoError(t, err)
			for _, key := range []string{"skali.dev/managed", "skali.dev/environment", identityAnnotation} {
				for _, remove := range []bool{false, true} {
					section := "labels"
					if key == identityAnnotation {
						section = "annotations"
					}
					var value any = "changed"
					if remove {
						value = nil
					}
					patch, _ := json.Marshal(map[string]any{"metadata": map[string]any{section: map[string]any{key: value}}})
					_, err = resource.Patch(ctx, applied.Live.GetName(), types.MergePatchType, patch, metav1.PatchOptions{})
					require.True(t, apierrors.IsForbidden(err), "%s %s removal=%t: %v", applied.Live.GetKind(), key, remove, err)
				}
			}
			forced := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": applied.Live.GetAPIVersion(), "kind": applied.Live.GetKind(),
				"metadata": map[string]any{"name": applied.Live.GetName(), "namespace": applied.Live.GetNamespace(), "labels": map[string]any{"skali.dev/environment": "foreign"}},
			}}
			patch, err := forced.MarshalJSON()
			require.NoError(t, err)
			_, err = resource.Patch(ctx, applied.Live.GetName(), types.ApplyPatchType, patch, metav1.PatchOptions{FieldManager: "ownership-test", Force: new(true)})
			require.True(t, apierrors.IsForbidden(err), "forced apply cannot change identity: %v", err)
			_, err = resource.Patch(ctx, applied.Live.GetName(), types.MergePatchType, []byte(`{"metadata":{"annotations":{"unrelated":"allowed"},"finalizers":["skali.dev/test"]}}`), metav1.PatchOptions{})
			require.NoError(t, err)
			_, err = resource.Patch(ctx, applied.Live.GetName(), types.MergePatchType, []byte(`{"metadata":{"finalizers":null}}`), metav1.PatchOptions{})
			require.NoError(t, err)
		}
	})

	t.Run("field transfer and finalization", func(t *testing.T) {
		labels := map[string]string{"app": "field-transfer"}
		deployment := &appsv1.Deployment{TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"}, ObjectMeta: metav1.ObjectMeta{Name: "field-transfer", Namespace: ns, Labels: namespace.Labels}, Spec: appsv1.DeploymentSpec{
			Replicas: new(int32(2)), Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web", Image: "traefik/whoami:v1.10.2"}}}},
		}}
		first, err := client.Apply(ctx, deployment, false)
		require.NoError(t, err)
		ref := ObjectRef{GVK: deployment.GroupVersionKind(), Namespace: ns, Name: deployment.Name, UID: first.Live.GetUID()}
		require.NoError(t, client.DisownFields(ctx, ref, FieldManagerProject, ReplicasFieldPath))
		deployment.Spec.Replicas = nil
		second, err := client.Apply(ctx, deployment, false)
		require.NoError(t, err)
		require.False(t, OwnsField(second.Live.GetManagedFields(), FieldManagerProject, ReplicasFieldPath))
		deployment.Spec.Replicas = new(int32(3))
		third, err := client.Apply(ctx, deployment, true)
		require.NoError(t, err)
		require.True(t, OwnsField(third.Live.GetManagedFields(), FieldManagerProject, ReplicasFieldPath))
		liveNS, err := client.Clientset.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
		require.NoError(t, err)
		_, err = client.Clientset.CoreV1().Namespaces().Finalize(ctx, liveNS, metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}})
		require.NoError(t, err, "ownership policy must allow namespace finalization")
	})

	t.Run("replacement UID", func(t *testing.T) {
		want := &corev1.ConfigMap{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"}, ObjectMeta: metav1.ObjectMeta{Name: "replacement", Namespace: ns, Labels: namespace.Labels}, Data: map[string]string{"value": "before"}}
		old, err := client.Apply(ctx, want, false)
		require.NoError(t, err)
		require.NoError(t, client.Clientset.CoreV1().ConfigMaps(ns).Delete(ctx, want.Name, metav1.DeleteOptions{}))
		_, err = client.Apply(ctx, want, false)
		require.NoError(t, err)
		intended, resource, err := client.prepare(want)
		require.NoError(t, err)
		stampIdentity(intended)
		intended.SetUID(old.Live.GetUID())
		intended.Object["data"] = map[string]any{"value": "after"}
		data, err := intended.MarshalJSON()
		require.NoError(t, err)
		_, err = resource.Patch(ctx, want.Name, types.ApplyPatchType, data, metav1.PatchOptions{FieldManager: FieldManagerProject})
		require.Error(t, err)
		live, err := client.Clientset.CoreV1().ConfigMaps(ns).Get(ctx, want.Name, metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, "before", live.Data["value"])
		require.NoError(t, client.Clientset.CoreV1().ConfigMaps(ns).Delete(ctx, want.Name, metav1.DeleteOptions{}))
		_, err = resource.Patch(ctx, want.Name, types.ApplyPatchType, data, metav1.PatchOptions{FieldManager: FieldManagerProject})
		require.Error(t, err, "UID-guarded apply must not implicitly create")
	})
}
