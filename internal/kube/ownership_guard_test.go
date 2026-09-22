package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	kt "k8s.io/client-go/testing"
)

func checkedPolicy() *admissionv1.ValidatingAdmissionPolicy {
	policy := OwnershipPolicy()
	policy.Generation = 1
	policy.ResourceVersion = "1"
	policy.Status = admissionv1.ValidatingAdmissionPolicyStatus{ObservedGeneration: 1, TypeChecking: &admissionv1.TypeChecking{}}
	return policy
}

func TestOwnershipPolicyContract(t *testing.T) {
	require.NoError(t, validateOwnershipPolicy(checkedPolicy(), OwnershipPolicyBinding()))
	for _, tc := range []struct {
		name   string
		change func(*admissionv1.ValidatingAdmissionPolicy, *admissionv1.ValidatingAdmissionPolicyBinding)
	}{
		{"weakened failure policy", func(p *admissionv1.ValidatingAdmissionPolicy, _ *admissionv1.ValidatingAdmissionPolicyBinding) {
			p.Spec.FailurePolicy = new(admissionv1.Ignore)
		}},
		{"changed match", func(p *admissionv1.ValidatingAdmissionPolicy, _ *admissionv1.ValidatingAdmissionPolicyBinding) {
			p.Spec.MatchConditions = nil
		}},
		{"warning binding", func(_ *admissionv1.ValidatingAdmissionPolicy, b *admissionv1.ValidatingAdmissionPolicyBinding) {
			b.Spec.ValidationActions = []admissionv1.ValidationAction{admissionv1.Warn}
		}},
		{"unobserved generation", func(p *admissionv1.ValidatingAdmissionPolicy, _ *admissionv1.ValidatingAdmissionPolicyBinding) {
			p.Generation++
		}},
		{"not checked", func(p *admissionv1.ValidatingAdmissionPolicy, _ *admissionv1.ValidatingAdmissionPolicyBinding) {
			p.Status.TypeChecking = nil
		}},
		{"type warning", func(p *admissionv1.ValidatingAdmissionPolicy, _ *admissionv1.ValidatingAdmissionPolicyBinding) {
			p.Status.TypeChecking.ExpressionWarnings = []admissionv1.ExpressionWarning{{Warning: "invalid"}}
		}},
		{"deleting binding", func(_ *admissionv1.ValidatingAdmissionPolicy, b *admissionv1.ValidatingAdmissionPolicyBinding) {
			b.DeletionTimestamp = new(metav1.Now())
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, b := checkedPolicy(), OwnershipPolicyBinding()
			tc.change(p, b)
			require.Error(t, validateOwnershipPolicy(p, b))
		})
	}
}

func TestOwnershipProbeRequiresActualPolicyDenial(t *testing.T) {
	for _, mode := range []string{"enforced", "unenforced", "other denial", "unchanged denied", "missing"} {
		t.Run(mode, func(t *testing.T) {
			fake := kubefake.NewClientset(checkedPolicy(), OwnershipPolicyBinding(), OwnershipProbe())
			calls := 0
			fake.PrependReactor("update", "configmaps", func(action kt.Action) (bool, runtime.Object, error) {
				calls++
				update := action.(kt.UpdateActionImpl)
				require.Equal(t, []string{metav1.DryRunAll}, update.GetUpdateOptions().DryRun)
				cm := update.GetObject().(*corev1.ConfigMap)
				if mode == "unenforced" || (calls == 1 && mode != "unchanged denied") {
					return true, cm, nil
				}
				message := OwnershipPolicyName + ": " + ownershipDenied
				if mode == "other denial" {
					message = "another policy"
				}
				return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, cm.Name, errors.New(message))
			})
			if mode == "missing" {
				require.NoError(t, fake.AdmissionregistrationV1().ValidatingAdmissionPolicies().Delete(context.Background(), OwnershipPolicyName, metav1.DeleteOptions{}))
			}
			c := &Client{Clientset: fake}
			err := c.VerifyOwnershipPolicy(context.Background())
			if mode == "enforced" {
				require.NoError(t, err)
				require.Equal(t, 4, calls)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestOwnershipServerAddresses(t *testing.T) {
	slices := &discoveryv1.EndpointSliceList{Items: []discoveryv1.EndpointSlice{{Ports: []discoveryv1.EndpointPort{{Name: new("https"), Port: new(int32(6443))}}, Endpoints: []discoveryv1.Endpoint{
		{Addresses: []string{"10.0.0.2", "10.0.0.1"}},
		{Addresses: []string{"10.0.0.3"}, Conditions: discoveryv1.EndpointConditions{Ready: new(false)}},
		{Addresses: []string{"10.0.0.4"}, Conditions: discoveryv1.EndpointConditions{Terminating: new(true)}},
	}}}}
	addresses, err := ownershipServerAddresses(slices)
	require.NoError(t, err)
	require.Equal(t, []string{"10.0.0.1:6443", "10.0.0.2:6443"}, addresses)
	_, err = ownershipServerAddresses(&discoveryv1.EndpointSliceList{})
	require.Error(t, err)
}

// A real HTTP client exercises peer addressing and probes; Kubernetes watches
// use a fake client so events and disconnections are deterministic.
func TestOwnershipMonitorVerifiesEveryPeerAndInvalidates(t *testing.T) {
	for _, event := range []string{"policy", "binding", "membership", "disconnect", "cancel", "unverified peer"} {
		t.Run(event, func(t *testing.T) {
			var peerCalls [2]int
			servers := make([]*httptest.Server, 2)
			for i := range servers {
				i := i
				servers[i] = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					var object any
					switch {
					case strings.Contains(r.URL.Path, "validatingadmissionpolicybindings"):
						object = OwnershipPolicyBinding()
					case strings.Contains(r.URL.Path, "validatingadmissionpolicies"):
						object = checkedPolicy()
					case r.Method == http.MethodGet:
						object = OwnershipProbe()
					default:
						peerCalls[i]++
						var cm corev1.ConfigMap
						require.NoError(t, json.NewDecoder(r.Body).Decode(&cm))
						if peerCalls[i] > 1 && !(event == "unverified peer" && i == 1) {
							w.WriteHeader(http.StatusForbidden)
							object = &metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: metav1.StatusFailure, Code: 403, Reason: metav1.StatusReasonForbidden, Message: OwnershipPolicyName + ": " + ownershipDenied}
						} else {
							object = &cm
						}
					}
					require.NoError(t, json.NewEncoder(w).Encode(object))
				}))
				defer servers[i].Close()
			}
			slices := &discoveryv1.EndpointSliceList{ListMeta: metav1.ListMeta{ResourceVersion: "1"}}
			for i, server := range servers {
				host := strings.TrimPrefix(server.URL, "https://")
				// Both test peers are localhost, with distinct ports.
				var port int32
				_, err := fmt.Sscanf(host, "127.0.0.1:%d", &port)
				require.NoError(t, err)
				slices.Items = append(slices.Items, discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprint(i), Namespace: "default", Labels: map[string]string{"kubernetes.io/service-name": "kubernetes"}}, Ports: []discoveryv1.EndpointPort{{Name: new("https"), Port: &port}}, Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"127.0.0.1"}}}})
			}
			objects := []runtime.Object{checkedPolicy(), OwnershipPolicyBinding()}
			for i := range slices.Items {
				objects = append(objects, &slices.Items[i])
			}
			fake := kubefake.NewClientset(objects...)
			observers := map[string]*watch.RaceFreeFakeWatcher{}
			for _, resource := range []string{"validatingadmissionpolicies", "validatingadmissionpolicybindings", "endpointslices"} {
				w := watch.NewRaceFreeFake()
				observers[resource] = w
				fake.PrependWatchReactor(resource, func(kt.Action) (bool, watch.Interface, error) { return true, w, nil })
			}
			c := &Client{Clientset: fake, Config: &rest.Config{Host: servers[0].URL, ContentConfig: rest.ContentConfig{ContentType: "application/json", AcceptContentTypes: "application/json"}, TLSClientConfig: rest.TLSClientConfig{Insecure: true}}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- c.watchOwnershipProtection(ctx) }()
			if event == "unverified peer" {
				select {
				case err := <-done:
					require.Error(t, err)
				case <-time.After(5 * time.Second):
					t.Fatal("verification did not finish")
				}
				require.Nil(t, c.ownership.Load())
				return
			}
			require.Eventually(t, func() bool { g := c.ownership.Load(); return g != nil && g.valid.Load() }, 5*time.Second, 10*time.Millisecond)
			switch event {
			case "policy":
				observers["validatingadmissionpolicies"].Modify(checkedPolicy())
			case "binding":
				observers["validatingadmissionpolicybindings"].Delete(OwnershipPolicyBinding())
			case "membership":
				observers["endpointslices"].Add(&slices.Items[0])
			case "disconnect":
				observers["validatingadmissionpolicies"].Stop()
			case "cancel":
				cancel()
			}
			select {
			case err := <-done:
				require.Error(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("watch did not invalidate")
			}
			require.False(t, c.ownership.Load().valid.Load())
		})
	}
}
