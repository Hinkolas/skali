// Package proxytrust identifies the immediate peers allowed to report client
// addresses. A Kubernetes pod CIDR is never a proxy identity: application pods
// share that network with the platform's reverse proxies.
package proxytrust

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const refreshInterval = 5 * time.Second
const maxAge = 10 * time.Second

// Parse accepts comma-separated CIDRs for operator-managed reverse proxies.
func Parse(value string) ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	if strings.TrimSpace(value) == "" {
		return prefixes, nil
	}
	for _, raw := range strings.Split(value, ",") {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("SKALI_TRUSTED_PROXIES: invalid CIDR %q", raw)
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
}

type Trust struct {
	prefixes []netip.Prefix
	mu       sync.RWMutex
	pods     map[netip.Addr]struct{}
	updated  time.Time
}

func New(prefixes []netip.Prefix) *Trust {
	return &Trust{prefixes: append([]netip.Prefix(nil), prefixes...)}
}

func (t *Trust) Contains(ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, prefix := range t.prefixes {
		if prefix.Contains(ip) {
			return true
		}
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	_, ok := t.pods[ip]
	return ok && time.Since(t.updated) < maxAge
}

// Run refreshes only platform proxy pod addresses, in administrator-owned
// namespaces. Failure clears dynamic trust; a stalled API also expires it.
// Requests keep working with the socket peer as their address in that case.
func (t *Trust) Run(ctx context.Context, client kubernetes.Interface) {
	t.refresh(ctx, client)
	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.refresh(ctx, client)
		}
	}
}

func (t *Trust) refresh(ctx context.Context, client kubernetes.Interface) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ips := map[netip.Addr]struct{}{}
	for _, proxy := range []struct{ namespace, name string }{
		{"kube-system", "traefik"}, {"skali-system", "skali-web"},
	} {
		pods, err := client.CoreV1().Pods(proxy.namespace).List(ctx, metav1.ListOptions{
			LabelSelector: "app.kubernetes.io/name=" + proxy.name,
		})
		if err != nil {
			slog.WarnContext(ctx, "proxy discovery failed; ignoring forwarded client addresses", "error", err)
			ips = nil
			break
		}
		for _, pod := range pods.Items {
			// Never turn a host's address into proxy authority.
			if pod.Spec.HostNetwork || pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning {
				continue
			}
			for _, podIP := range pod.Status.PodIPs {
				if ip, err := netip.ParseAddr(podIP.IP); err == nil {
					ips[ip.Unmap()] = struct{}{}
				}
			}
		}
	}
	t.mu.Lock()
	t.pods, t.updated = ips, time.Now()
	t.mu.Unlock()
}
