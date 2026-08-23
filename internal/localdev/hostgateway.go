package localdev

// The host gateway name (host.k3d.internal) is how workloads reach the
// host machine: skalid resolves it at startup to render the gateway that
// carries dev intercepts to processes on the host. k3d injects the entry
// into the CoreDNS NodeHosts configmap and the node's /etc/hosts only
// during its own cluster create and start, and both stores are volatile:
// k3s owns the NodeHosts key and rewrites it from the Node objects on
// boot, and docker regenerates /etc/hosts on every container restart. The
// raw docker stop/start cycles of the node-IP pin and repair (nodeip.go)
// therefore silently drop the entry, so every ensure pass verifies both
// stores against the live gateway address and repairs them in place.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/Hinkolas/skali/internal/kube"
)

const hostGatewayName = "host.k3d.internal"

// ensureHostGateway proves the host gateway name resolves inside the
// cluster, repairing the CoreDNS NodeHosts key and the node container's
// /etc/hosts when a node restart dropped the entry.
func ensureHostGateway(ctx context.Context, client *kube.Client, progress Progress) error {
	ip, err := hostGatewayIP(ctx)
	if err != nil {
		return err
	}
	hostsRepaired, err := ensureNodeEtcHosts(ctx, ip)
	if err != nil {
		return err
	}
	dnsRepaired, err := ensureCoreDNSHosts(ctx, client, ip)
	if err != nil {
		return err
	}
	if hostsRepaired || dnsRepaired {
		progress.Start("Repair host gateway")
		progress.Note(hostGatewayName + " had dropped out of the cluster DNS")
		progress.Done(hostGatewayName + " is " + ip)
	}
	return nil
}

// hostGatewayIP recovers the address the node reaches the host machine
// at, the way k3d does: host.docker.internal resolved inside the node
// container (Docker Desktop and rootless engines), falling back to the
// cluster network's gateway (native Linux engines, where the name does
// not exist).
func hostGatewayIP(ctx context.Context) (string, error) {
	if out, err := exec.CommandContext(ctx, "docker", "exec", nodeContainer(),
		"getent", "ahostsv4", "host.docker.internal").Output(); err == nil {
		if fields := strings.Fields(string(out)); len(fields) > 0 && net.ParseIP(fields[0]) != nil {
			return fields[0], nil
		}
	}
	// The k3s image may lack getent; busybox ping prints the resolved
	// address either way.
	if out, err := exec.CommandContext(ctx, "docker", "exec", nodeContainer(),
		"ping", "-4", "-c", "1", "-W", "1", "host.docker.internal").CombinedOutput(); err == nil {
		if ip := pingResolvedIP(string(out)); ip != "" {
			return ip, nil
		}
	}
	raw, err := inspectNetworkRaw(ctx)
	if err != nil {
		return "", err
	}
	spec, err := parseNetworkSpec(raw, nodeContainer())
	if err != nil {
		return "", err
	}
	if spec.Gateway == "" {
		return "", fmt.Errorf("localdev: could not determine the host address behind %s", hostGatewayName)
	}
	return spec.Gateway, nil
}

var pingResolvedPattern = regexp.MustCompile(`\((\d+\.\d+\.\d+\.\d+)\)`)

// pingResolvedIP extracts the resolved address from a ping banner like
// "PING host.docker.internal (192.168.65.254): 56 data bytes".
func pingResolvedIP(out string) string {
	match := pingResolvedPattern.FindStringSubmatch(out)
	if match == nil {
		return ""
	}
	return match[1]
}

// withGatewayEntry returns the hosts-format content with exactly one
// gateway entry at the given address, and whether anything had to change:
// stale entries are dropped, a missing one is appended, and a correct one
// leaves the content untouched.
func withGatewayEntry(content, ip string) (updated string, changed bool) {
	var kept []string
	found := false
	if trimmed := strings.TrimRight(content, "\n"); trimmed != "" {
		kept = make([]string, 0, strings.Count(trimmed, "\n")+2)
		for line := range strings.SplitSeq(trimmed, "\n") {
			fields := strings.Fields(line)
			carries := false
			for i, field := range fields {
				if i > 0 && field == hostGatewayName {
					carries = true
					break
				}
			}
			if !carries {
				kept = append(kept, line)
				continue
			}
			if fields[0] == ip && !found {
				kept = append(kept, line)
				found = true
				continue
			}
			changed = true
		}
	}
	if !found {
		kept = append(kept, ip+" "+hostGatewayName)
		changed = true
	}
	return strings.Join(kept, "\n") + "\n", changed
}

// ensureNodeEtcHosts keeps the gateway entry in the node container's
// /etc/hosts, which docker regenerates without it on every restart. The
// file is a bind mount, so it is rewritten in place, never replaced.
func ensureNodeEtcHosts(ctx context.Context, ip string) (bool, error) {
	out, err := exec.CommandContext(ctx, "docker", "exec", nodeContainer(),
		"cat", "/etc/hosts").Output()
	if err != nil {
		return false, fmt.Errorf("localdev: read node /etc/hosts: %w", err)
	}
	updated, changed := withGatewayEntry(string(out), ip)
	if !changed {
		return false, nil
	}
	write := exec.CommandContext(ctx, "docker", "exec", "-i", nodeContainer(),
		"sh", "-c", "cat > /etc/hosts")
	write.Stdin = strings.NewReader(updated)
	if out, err := write.CombinedOutput(); err != nil {
		return false, fmt.Errorf("localdev: write node /etc/hosts: %w\n%s", err, out)
	}
	return true, nil
}

// ensureCoreDNSHosts keeps the gateway entry in the NodeHosts key of the
// coredns configmap, which k3s rewrites from the Node objects on boot.
// The merge patch touches only that key and cannot conflict; the CoreDNS
// hosts plugin picks the change up within its reload window. A missing
// configmap (k3s still deploying it on a fresh boot) is left for the
// next pass.
func ensureCoreDNSHosts(ctx context.Context, client *kube.Client, ip string) (bool, error) {
	maps := client.Clientset.CoreV1().ConfigMaps("kube-system")
	current, err := maps.Get(ctx, "coredns", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("localdev: read coredns configmap: %w", err)
	}
	updated, changed := withGatewayEntry(current.Data["NodeHosts"], ip)
	if !changed {
		return false, nil
	}
	patch, err := json.Marshal(map[string]any{"data": map[string]string{"NodeHosts": updated}})
	if err != nil {
		return false, fmt.Errorf("localdev: encode coredns patch: %w", err)
	}
	if _, err := maps.Patch(ctx, "coredns", types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return false, fmt.Errorf("localdev: patch coredns NodeHosts: %w", err)
	}
	return true, nil
}
