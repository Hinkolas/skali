package kube

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type ownershipProtection struct {
	valid   atomic.Bool
	context context.Context
	server  *Client
}

// protectedResource pins the request to a verified API server. Routing a
// version-free request through the load balancer would allow a newly joined,
// not-yet-verified server to receive it before the membership watch arrives.
func (c *Client) protectedResource(gvk schema.GroupVersionKind, namespace string) dynamic.ResourceInterface {
	guard := c.ownership.Load()
	if guard == nil || !guard.valid.Load() || guard.context.Err() != nil {
		return nil
	}
	resource, err := guard.server.resource(gvk, namespace)
	if err != nil {
		return nil
	}
	return resource
}

func validateOwnershipPolicy(policy *admissionv1.ValidatingAdmissionPolicy, binding *admissionv1.ValidatingAdmissionPolicyBinding) error {
	if policy.DeletionTimestamp != nil || binding.DeletionTimestamp != nil ||
		!equality.Semantic.DeepEqual(policy.Spec, OwnershipPolicy().Spec) ||
		!equality.Semantic.DeepEqual(binding.Spec, OwnershipPolicyBinding().Spec) {
		return fmt.Errorf("kube: ownership policy or binding differs from the expected contract")
	}
	if policy.Status.ObservedGeneration != policy.Generation || policy.Status.TypeChecking == nil || len(policy.Status.TypeChecking.ExpressionWarnings) != 0 {
		return fmt.Errorf("kube: ownership policy type checking is not complete and clean")
	}
	return nil
}

// VerifyOwnershipPolicy checks both configuration and actual enforcement on this
// client's API endpoint. A policy merely existing does not prove it is active.
func (c *Client) VerifyOwnershipPolicy(ctx context.Context) error {
	policies := c.Clientset.AdmissionregistrationV1()
	policy, err := policies.ValidatingAdmissionPolicies().Get(ctx, OwnershipPolicyName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	binding, err := policies.ValidatingAdmissionPolicyBindings().Get(ctx, OwnershipPolicyName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if err := validateOwnershipPolicy(policy, binding); err != nil {
		return err
	}
	maps := c.Clientset.CoreV1().ConfigMaps(OwnershipProbeNamespace)
	probe, err := maps.Get(ctx, OwnershipProbeName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	expected := OwnershipProbe()
	if probe.DeletionTimestamp != nil || probe.Labels["skali.dev/managed"] != expected.Labels["skali.dev/managed"] ||
		probe.Labels["skali.dev/environment"] != expected.Labels["skali.dev/environment"] ||
		probe.Annotations[identityAnnotation] != expected.Annotations[identityAnnotation] {
		return fmt.Errorf("kube: ownership probe identity differs")
	}
	options := metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}}
	if _, err := maps.Update(ctx, probe.DeepCopy(), options); err != nil {
		return fmt.Errorf("kube: ownership probe unchanged update: %w", err)
	}
	for _, key := range []string{"skali.dev/managed", "skali.dev/environment", identityAnnotation} {
		changed := probe.DeepCopy()
		if key == identityAnnotation {
			changed.Annotations[key] = "changed"
		} else {
			changed.Labels[key] = "changed"
		}
		_, err := maps.Update(ctx, changed, options)
		if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), OwnershipPolicyName) || !strings.Contains(err.Error(), ownershipDenied) {
			return fmt.Errorf("kube: ownership probe %s did not receive the expected policy denial: %v", key, err)
		}
	}
	return nil
}

// MonitorOwnershipProtection is optional: absent policies, insufficient RBAC,
// unreachable peers, and observation failures all leave version checks enabled.
// The installer owns the policy lifecycle. This is not a security boundary
// against administrators who can disable admission itself.
func (c *Client) MonitorOwnershipProtection(ctx context.Context) {
	for ctx.Err() == nil {
		err := c.watchOwnershipProtection(ctx)
		c.ownership.Store(nil)
		if ctx.Err() != nil {
			return
		}
		slog.DebugContext(ctx, "ownership protection unavailable; retaining resource version checks", "error", err)
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (c *Client) watchOwnershipProtection(ctx context.Context) error {
	if c.Config == nil {
		return fmt.Errorf("kube: no configuration for ownership verification")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// A bounded watch lifetime is also a lease: a silently broken observation
	// connection cannot enable this optimization indefinitely.
	ctx, deadline := context.WithTimeout(ctx, time.Minute)
	defer deadline()
	policyAPI := c.Clientset.AdmissionregistrationV1()
	policy, err := policyAPI.ValidatingAdmissionPolicies().Get(ctx, OwnershipPolicyName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	binding, err := policyAPI.ValidatingAdmissionPolicyBindings().Get(ctx, OwnershipPolicyName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if err := validateOwnershipPolicy(policy, binding); err != nil {
		return err
	}
	slices, err := c.Clientset.DiscoveryV1().EndpointSlices("default").List(ctx, metav1.ListOptions{LabelSelector: "kubernetes.io/service-name=kubernetes"})
	if err != nil {
		return err
	}
	addresses, err := ownershipServerAddresses(slices)
	if err != nil {
		return err
	}
	timeout := int64(60)
	options := metav1.ListOptions{FieldSelector: "metadata.name=" + OwnershipPolicyName, ResourceVersion: policy.ResourceVersion, TimeoutSeconds: &timeout}
	policyWatch, err := policyAPI.ValidatingAdmissionPolicies().Watch(ctx, options)
	if err != nil {
		return err
	}
	defer policyWatch.Stop()
	options.ResourceVersion = binding.ResourceVersion
	bindingWatch, err := policyAPI.ValidatingAdmissionPolicyBindings().Watch(ctx, options)
	if err != nil {
		return err
	}
	defer bindingWatch.Stop()
	membershipWatch, err := c.Clientset.DiscoveryV1().EndpointSlices("default").Watch(ctx, metav1.ListOptions{LabelSelector: "kubernetes.io/service-name=kubernetes", ResourceVersion: slices.ResourceVersion, TimeoutSeconds: &timeout})
	if err != nil {
		return err
	}
	defer membershipWatch.Stop()
	guard := &ownershipProtection{context: ctx}
	defer guard.valid.Store(false)
	// Drain each watch while verification is in flight. Any event, including a
	// watch error or closure, cancels verification before it can enable the guard.
	for _, observer := range []watch.Interface{policyWatch, bindingWatch, membershipWatch} {
		go func(w watch.Interface) {
			for {
				select {
				case <-ctx.Done():
					guard.valid.Store(false)
					return
				case event, ok := <-w.ResultChan():
					if ok && event.Type == watch.Bookmark {
						continue
					}
					guard.valid.Store(false)
					cancel()
					return
				}
			}
		}(observer)
	}
	for _, address := range addresses {
		peer, err := c.ownershipPeer(address)
		if err != nil {
			return err
		}
		probeCtx, stopProbe := context.WithTimeout(ctx, 5*time.Second)
		err = peer.VerifyOwnershipPolicy(probeCtx)
		stopProbe()
		if err != nil {
			return fmt.Errorf("kube: ownership protection on %s: %w", address, err)
		}
		if guard.server == nil {
			guard.server = peer
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	guard.valid.Store(true)
	c.ownership.Store(guard)
	slog.DebugContext(ctx, "ownership protection verified", "api_servers", len(addresses))
	// Recheck after publication too: cancellation may race the stores above.
	if ctx.Err() != nil {
		guard.valid.Store(false)
	}
	<-ctx.Done()
	return ctx.Err()
}

func ownershipServerAddresses(slices *discoveryv1.EndpointSliceList) ([]string, error) {
	addresses := map[string]bool{}
	for _, slice := range slices.Items {
		for _, port := range slice.Ports {
			if port.Name == nil || *port.Name != "https" || port.Port == nil {
				continue
			}
			for _, endpoint := range slice.Endpoints {
				if endpoint.Conditions.Ready != nil && !*endpoint.Conditions.Ready {
					continue
				}
				if endpoint.Conditions.Terminating != nil && *endpoint.Conditions.Terminating {
					continue
				}
				for _, address := range endpoint.Addresses {
					if net.ParseIP(address) == nil {
						return nil, fmt.Errorf("kube: API endpoint is not an IP address")
					}
					addresses[net.JoinHostPort(address, strconv.Itoa(int(*port.Port)))] = true
				}
			}
		}
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("kube: no serving API endpoints discovered")
	}
	result := make([]string, 0, len(addresses))
	for address := range addresses {
		result = append(result, address)
	}
	sort.Strings(result)
	return result, nil
}

func (c *Client) ownershipPeer(address string) (*Client, error) {
	config := rest.CopyConfig(c.Config)
	original, err := url.Parse(config.Host)
	if err != nil {
		return nil, err
	}
	if config.ServerName == "" {
		config.ServerName = original.Hostname()
	}
	config.Host = "https://" + address
	// Keep certificate validation, credentials and transport wrappers intact.
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return &Client{Config: config, Clientset: clientset, Dynamic: dyn, Mapper: c.Mapper}, nil
}
