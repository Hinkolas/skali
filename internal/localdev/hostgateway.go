package localdev

// The host gateway name (host.k3d.internal) is how workloads reach the
// host machine: skalid resolves it to render the gateway that carries dev
// intercepts to processes on the host. k3d injects the entry into the
// node's /etc/hosts and, for CoreDNS, rewrites the NodeHosts key inside
// k3s's on-disk coredns manifest (/var/lib/rancher/k3s/server/manifests/
// coredns.yaml) once during cluster create, with no retry and no check.
// Both stores are volatile: docker regenerates /etc/hosts on every
// container restart, and k3s re-stages its bundled manifests from its
// embedded copies on every boot, which discards k3d's rewrite. The node-IP
// pin at create (nodeip.go) restarts the node seconds after k3d returns,
// so a fresh cluster's CoreDNS configmap is the stock one without the
// entry; a later crash-loop repair loses it the same way. Every ensure
// pass therefore verifies both stores against the live gateway address,
// repairs them in place, and proves the name resolves inside the cluster
// before the platform counts as ready.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/Hinkolas/skali/internal/kube"
)

const hostGatewayName = "host.k3d.internal"

// coreDNSConfigMapWait bounds the wait for k3s to deploy the coredns
// configmap on a fresh boot; measured at about ten seconds after the
// apiserver answers.
const coreDNSConfigMapWait = 2 * time.Minute

// hostGatewayDNSWait bounds the wait for CoreDNS to serve a repaired entry:
// the kubelet propagates the configmap to the pod within its sync period
// and the hosts plugin reloads every 15 seconds after that.
const hostGatewayDNSWait = 2 * time.Minute

// ensureHostGateway keeps the host gateway entry in the CoreDNS NodeHosts
// key and the node container's /etc/hosts, repairing whichever a restart
// dropped, and reports whether anything had to change; a repair means the
// cluster DNS still has to be proven before the platform is ready (see
// verifyHostGateway).
func ensureHostGateway(ctx context.Context, client *kube.Client, progress Progress) (repaired bool, err error) {
	ip, err := hostGatewayIP(ctx)
	if err != nil {
		return false, err
	}
	hostsRepaired, err := ensureNodeEtcHosts(ctx, ip)
	if err != nil {
		return false, err
	}
	dnsRepaired, err := ensureCoreDNSHosts(ctx, client, ip)
	if err != nil {
		return false, err
	}
	if !hostsRepaired && !dnsRepaired {
		return false, nil
	}
	progress.Start("Repair host gateway")
	switch {
	case hostsRepaired && dnsRepaired:
		progress.Note(hostGatewayName + " was missing from the node hosts file and the cluster DNS")
	case dnsRepaired:
		progress.Note(hostGatewayName + " was missing from the cluster DNS")
	default:
		progress.Note(hostGatewayName + " was missing from the node hosts file")
	}
	progress.Done(hostGatewayName + " is " + ip)
	return true, nil
}

// verifyHostGateway proves the host gateway name resolves through the
// cluster DNS, the way skalid will resolve it: a lookup against the
// kube-dns service from inside the node, polled until CoreDNS has picked
// the repaired entry up. It runs at the end of an ensure pass that
// repaired the entry, so the propagation window overlaps the platform
// work instead of adding to it.
func verifyHostGateway(ctx context.Context, client *kube.Client, progress Progress) error {
	ip, err := hostGatewayIP(ctx)
	if err != nil {
		return err
	}
	dns, err := client.Clientset.CoreV1().Services("kube-system").Get(ctx, "kube-dns", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("localdev: read kube-dns service: %w", err)
	}
	progress.Start("Verify host gateway DNS")
	deadline := time.Now().Add(hostGatewayDNSWait)
	for {
		out, _ := exec.CommandContext(ctx, "docker", "exec", nodeContainer(),
			"nslookup", hostGatewayName, dns.Spec.ClusterIP).CombinedOutput()
		if nslookupResolved(string(out), hostGatewayName, ip) {
			progress.Done(hostGatewayName + " resolves to " + ip + " in the cluster")
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("localdev: %s does not resolve in the cluster DNS %s after the NodeHosts repair; "+
				"last answer:\n%s", hostGatewayName, hostGatewayDNSWait, strings.TrimSpace(string(out)))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// nslookupResolved reads a busybox nslookup answer for name at ip: the
// "Address:" line under the "Name:" line for the queried name, ignoring the
// server banner (whose Address line names the resolver itself).
func nslookupResolved(out, name, ip string) bool {
	named := false
	for line := range strings.SplitSeq(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "Name:":
			named = fields[1] == name
		case "Address:":
			if named && strings.TrimSuffix(fields[1], ":53") == ip {
				return true
			}
		}
	}
	return false
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
// coredns configmap. The merge patch touches only that key and cannot
// conflict; k3s's own NodeHosts controller preserves entries for other
// addresses, and the CoreDNS hosts plugin picks the change up within its
// reload window. On a fresh boot k3s deploys the configmap a few seconds
// after the apiserver answers, so a missing one is waited for: returning
// early here is how a fresh cluster once reached skalid without the entry.
func ensureCoreDNSHosts(ctx context.Context, client *kube.Client, ip string) (bool, error) {
	maps := client.Clientset.CoreV1().ConfigMaps("kube-system")
	current, err := maps.Get(ctx, "coredns", metav1.GetOptions{})
	deadline := time.Now().Add(coreDNSConfigMapWait)
	for apierrors.IsNotFound(err) {
		if time.Now().After(deadline) {
			return false, errors.New("localdev: k3s did not deploy the coredns configmap within " +
				coreDNSConfigMapWait.String())
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(time.Second):
		}
		current, err = maps.Get(ctx, "coredns", metav1.GetOptions{})
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
