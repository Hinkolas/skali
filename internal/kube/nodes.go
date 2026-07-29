package kube

import (
	"context"
	"fmt"
	"net/netip"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NodeProxyCIDRs returns the /32 source addresses skalid's traffic presents
// when it enters the pod network: for every control-plane node (the API
// server hosts), the first two addresses of its podCIDR. Cross-node traffic
// arrives from the flannel VTEP (the podCIDR's network address itself);
// same-node traffic from the cni0 gateway (network address + 1). Measured
// on k3s; node-local traffic additionally rides the kubelet-probe
// exemption. Compiled into the policy admitting skalid to the object
// store's fenced ports; never widen these to the whole pod CIDR, which
// would include every tenant pod and void the fence.
func (c *Client) NodeProxyCIDRs(ctx context.Context) ([]string, error) {
	list, err := c.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("kube: list nodes: %w", err)
	}
	var cidrs []string
	for i := range list.Items {
		node := &list.Items[i]
		if node.Labels["node-role.kubernetes.io/control-plane"] != "true" {
			continue
		}
		podCIDRs := node.Spec.PodCIDRs
		if len(podCIDRs) == 0 && node.Spec.PodCIDR != "" {
			podCIDRs = []string{node.Spec.PodCIDR}
		}
		for _, cidr := range podCIDRs {
			prefix, err := netip.ParsePrefix(cidr)
			if err != nil || !prefix.Addr().Is4() {
				continue
			}
			base := prefix.Masked().Addr()
			cidrs = append(cidrs, base.String()+"/32", base.Next().String()+"/32")
		}
	}
	sort.Strings(cidrs)
	return cidrs, nil
}
