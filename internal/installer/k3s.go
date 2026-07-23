package installer

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/registrytoken"
)

// Host paths owned by k3s and its install script.
const (
	K3sConfigDir      = "/etc/rancher/k3s"
	K3sConfigPath     = K3sConfigDir + "/config.yaml"
	K3sRegistriesPath = K3sConfigDir + "/registries.yaml"
	K3sBinaryPath     = "/usr/local/bin/k3s"
	K3sKubeconfigPath = K3sConfigDir + "/k3s.yaml"
	// K3sTokenPath holds the join token on agents, referenced from
	// config.yaml via token-file so the credential never rides the install
	// script's environment (the script persists every K3S_* variable into
	// the systemd env file).
	K3sTokenPath = K3sConfigDir + "/token"

	k3sUninstallScript      = "/usr/local/bin/k3s-uninstall.sh"
	k3sAgentUninstallScript = "/usr/local/bin/k3s-agent-uninstall.sh"
	k3sInstallScriptPath    = CacheDir + "/k3s-install.sh"

	// k3sAgentKubeletKubeconfig appears only after the agent's
	// token-authenticated registration with the server succeeded, making it
	// the host-side join proof on nodes that have no kube API access.
	k3sAgentKubeletKubeconfig = "/var/lib/rancher/k3s/agent/kubelet.kubeconfig"

	// K3sServerTokenPath is the permanent server token k3s generates on
	// every server. It is the only credential that can join an additional
	// server (it doubles as the passphrase for the bootstrap data a joining
	// server fetches), so `token --role server` reads it instead of minting
	// a TTL token.
	K3sServerTokenPath = "/var/lib/rancher/k3s/server/token"
	// k3sEtcdDBDir exists exactly when the server runs on embedded etcd;
	// legacy installs on the default sqlite datastore lack it and cannot
	// accept additional servers.
	k3sEtcdDBDir = "/var/lib/rancher/k3s/server/db/etcd"
)

// Vendored https://get.k3s.io; the script owns the systemd unit, selinux
// handling, and the generated uninstall scripts, so provisioning stays
// aligned with upstream behavior. Refreshing it is part of any k3s bump.
//
//go:embed assets/k3s-install.sh
var k3sInstallScript []byte

// k3sNode describes the node being provisioned. ServerURL and Token are
// set only when the node joins an existing cluster, as an agent or as an
// additional server.
type k3sNode struct {
	Name    string
	Cluster string
	// Role is the k3s role; empty means layout.RoleServer.
	Role         string
	Capabilities []string
	// NodeIP pins the advertised address on multi-homed hosts; empty keeps
	// the k3s default (the default-route interface).
	NodeIP string
	// ServerURL points a joining node at an existing server.
	ServerURL string
	// Token is the resolved join token plaintext; written to K3sTokenPath,
	// never passed through the environment.
	Token string
	// PullSecret is the cluster's shared registry pull credential, written
	// into registries.yaml so containerd can earn pull tokens once the
	// registry requires them. The first server generates it, joining nodes
	// receive it inside the composite join token; empty renders no
	// credential.
	PullSecret string
}

func (n k3sNode) role() string {
	if n.Role == "" {
		return layout.RoleServer
	}
	return n.Role
}

// k3sConfigYAML renders /etc/rancher/k3s/config.yaml. It is written before
// the install script runs so k3s starts configured: capability labels ride
// node registration, Traefik stays enabled as the edge, and on servers the
// embedded registry mirror (Spegel) keeps already-pulled images available
// while the managed registry is down. The first server initializes the
// embedded etcd cluster (cluster-init) so additional servers can join
// later without a datastore migration; joining nodes reference the server
// and the token file. embedded-registry is a server-only flag and would be
// fatal on an agent.
func k3sConfigYAML(node k3sNode) string {
	var builder strings.Builder
	builder.WriteString("node-name: " + node.Name + "\n")
	if node.NodeIP != "" {
		builder.WriteString("node-ip: " + node.NodeIP + "\n")
	}
	switch {
	case node.role() == layout.RoleAgent:
		builder.WriteString("server: " + node.ServerURL + "\n")
		builder.WriteString("token-file: " + K3sTokenPath + "\n")
	case node.ServerURL != "":
		builder.WriteString("server: " + node.ServerURL + "\n")
		builder.WriteString("token-file: " + K3sTokenPath + "\n")
		builder.WriteString("embedded-registry: true\n")
	default:
		builder.WriteString("cluster-init: true\n")
		builder.WriteString("embedded-registry: true\n")
	}
	builder.WriteString("node-label:\n")
	labels := layout.CapabilityLabels(node.Capabilities)
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		builder.WriteString("  - " + key + "=" + labels[key] + "\n")
	}
	builder.WriteString("  - " + layout.ClusterLabel + "=" + node.Cluster + "\n")
	return builder.String()
}

// k3sRegistriesYAML renders /etc/rancher/k3s/registries.yaml, written
// fully at install time: the wildcard mirror enables Spegel, and the
// managed-registry entry maps the constant internal host onto the
// node-local NodePort so containerd resolves production artifact
// references without DNS. The pull credential rides the configs section
// under both the registry name and the endpoint host, because containerd
// versions differ on which key credentials attach to.
func k3sRegistriesYAML(pullSecret string) string {
	configs := ""
	if pullSecret != "" {
		endpoint := fmt.Sprintf("127.0.0.1:%d", bundle.RegistryNodePort)
		configs = fmt.Sprintf(`configs:
  %q:
    auth:
      username: %s
      password: %q
  %q:
    auth:
      username: %s
      password: %q
`, endpoint, registrytoken.NodeUser, pullSecret,
			bundle.RegistryInternalHost, registrytoken.NodeUser, pullSecret)
	}
	return fmt.Sprintf(`mirrors:
  "*":
  %q:
    endpoint:
      - "http://127.0.0.1:%d"
`, bundle.RegistryInternalHost, bundle.RegistryNodePort) + configs
}

// installK3s writes the k3s configuration and runs the vendored install
// script pinned to K3sVersion. The agent role is selected by the explicit
// script argument, never by K3S_* environment variables: the script
// persists every exported K3S_* value into the systemd env file, which
// would leak the join token onto disk a second time.
func installK3s(ctx context.Context, runner host.Runner, node k3sNode, progress Progress) error {
	role := node.role()
	progress.Start("Install k3s " + K3sVersion + " (" + role + ")")
	if err := runner.MkdirAll(ctx, K3sConfigDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", K3sConfigDir, err)
	}
	if err := runner.WriteFile(ctx, K3sConfigPath, []byte(k3sConfigYAML(node)), 0o600); err != nil {
		return fmt.Errorf("write k3s config: %w", err)
	}
	if err := runner.WriteFile(ctx, K3sRegistriesPath, []byte(k3sRegistriesYAML(node.PullSecret)), 0o600); err != nil {
		return fmt.Errorf("write k3s registries config: %w", err)
	}
	if node.Token != "" {
		if err := runner.WriteFile(ctx, K3sTokenPath, []byte(node.Token+"\n"), 0o600); err != nil {
			return fmt.Errorf("write k3s join token: %w", err)
		}
	}
	if err := runner.MkdirAll(ctx, CacheDir, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", CacheDir, err)
	}
	if err := runner.WriteFile(ctx, k3sInstallScriptPath, k3sInstallScript, 0o700); err != nil {
		return fmt.Errorf("write k3s install script: %w", err)
	}
	args := []string{k3sInstallScriptPath}
	if role == layout.RoleAgent {
		args = append(args, "agent")
	}
	result, err := runner.Run(ctx, host.Command{
		Name: "sh",
		Args: args,
		Env:  []string{"INSTALL_K3S_VERSION=" + K3sVersion},
	})
	if err != nil {
		return fmt.Errorf("run k3s install script: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("k3s install script failed with exit code %d: %s",
			result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	progress.Done("")
	return nil
}

// upgradeK3s re-runs the vendored install script under the new pin. The
// script never touches /etc/rancher/k3s, so the config, registries, and
// token files written at install time survive untouched; the k3s unit uses
// KillMode=process, so workload containers keep running while the
// supervisor restarts. The script is re-staged from this binary first:
// refreshing it is part of any k3s bump. On SLE and CoreOS the script
// skips the service start (INSTALL_K3S_SKIP_START), which would time the
// follow-up wait out; the supported targets are Debian-family hosts.
func upgradeK3s(ctx context.Context, runner host.Runner, role string, progress Progress) error {
	progress.Start("Upgrade k3s to " + K3sVersion + " (" + role + ")")
	if err := runner.MkdirAll(ctx, CacheDir, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", CacheDir, err)
	}
	if err := runner.WriteFile(ctx, k3sInstallScriptPath, k3sInstallScript, 0o700); err != nil {
		return fmt.Errorf("write k3s install script: %w", err)
	}
	args := []string{k3sInstallScriptPath}
	if role == layout.RoleAgent {
		args = append(args, "agent")
	}
	result, err := runner.Run(ctx, host.Command{
		Name: "sh",
		Args: args,
		Env:  []string{"INSTALL_K3S_VERSION=" + K3sVersion},
	})
	if err != nil {
		return fmt.Errorf("run k3s install script: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("k3s install script failed with exit code %d: %s",
			result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	progress.Done("")
	return nil
}

// waitK3sUpgraded proves the upgraded k3s is back: the pinned version
// answers, the role's unit is active, and on servers the kube API serves
// /readyz, because the converge that follows calls the API with no retry.
// waitNodeReady is not reused here: it parses a single-node list and this
// wait must hold on multi-node clusters too.
func waitK3sUpgraded(ctx context.Context, runner host.Runner, role string, progress Progress) error {
	unit := "k3s.service"
	if role == layout.RoleAgent {
		unit = "k3s-agent.service"
	}
	progress.Start("Wait for k3s " + K3sVersion + " ready")
	deadline := time.Now().Add(5 * time.Minute)
	var lastDetail string
	for {
		lastDetail = probeK3sUpgraded(ctx, runner, role, unit)
		if lastDetail == "" {
			progress.Done("")
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("k3s never came back after the upgrade: %s", lastDetail)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// probeK3sUpgraded runs one round of the upgrade health checks; an empty
// return means everything holds.
func probeK3sUpgraded(ctx context.Context, runner host.Runner, role, unit string) string {
	if version := probeK3sVersion(ctx, runner); version != K3sVersion {
		return fmt.Sprintf("k3s reports version %q, expected %q", version, K3sVersion)
	}
	result, err := runner.Run(ctx, host.Command{
		Name: "systemctl", Args: []string{"is-active", unit},
	})
	if err != nil || result.ExitCode != 0 {
		return unit + " is not active"
	}
	if role != layout.RoleAgent {
		result, err := runner.Run(ctx, host.Command{
			Name: "k3s", Args: []string{"kubectl", "get", "--raw", "/readyz"},
		})
		if err != nil || result.ExitCode != 0 {
			return "the kubernetes api is not ready yet"
		}
	}
	return ""
}

// waitAgentJoined proves the join host-side: agents have no kube API
// access, so readiness and labels cannot be checked from here. The
// k3s-agent unit reporting active plus the kubelet kubeconfig existing
// means the token-authenticated registration with the server succeeded.
func waitAgentJoined(ctx context.Context, runner host.Runner, cluster, nodeName string, progress Progress) error {
	progress.Start("Join cluster " + fmt.Sprintf("%q", cluster))
	deadline := time.Now().Add(5 * time.Minute)
	var lastDetail string
	for {
		if time.Now().After(deadline) {
			if lastDetail == "" {
				lastDetail = "no status observed"
			}
			return fmt.Errorf("k3s agent never joined the cluster: %s", lastDetail)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
		result, err := runner.Run(ctx, host.Command{
			Name: "systemctl", Args: []string{"is-active", "k3s-agent.service"},
		})
		if err != nil || result.ExitCode != 0 {
			lastDetail = "k3s-agent.service is not active"
			continue
		}
		info, err := runner.Stat(ctx, k3sAgentKubeletKubeconfig)
		if err != nil || !info.Exists {
			lastDetail = "the agent has not completed registration with the server"
			continue
		}
		progress.Done("")
		break
	}
	// The capability labels ride node registration via the config written
	// before the install script ran; registration is exactly what the wait
	// above proved, so this step is a statement, not a probe.
	progress.Start("Stamp capability labels on node " + nodeName)
	progress.Done("")
	return nil
}

// waitServerJoined proves an additional server's join: the k3s unit is
// active, the local kube API answers /readyz (joining servers run a full
// control plane, unlike agents), and the own node object reports Ready
// with the stamped labels.
func waitServerJoined(ctx context.Context, runner host.Runner, cluster, nodeName string, capabilities []string, progress Progress) error {
	progress.Start("Join cluster " + fmt.Sprintf("%q", cluster))
	deadline := time.Now().Add(5 * time.Minute)
	var lastDetail string
	for {
		if time.Now().After(deadline) {
			if lastDetail == "" {
				lastDetail = "no status observed"
			}
			return fmt.Errorf("k3s server never joined the cluster: %s", lastDetail)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
		result, err := runner.Run(ctx, host.Command{
			Name: "systemctl", Args: []string{"is-active", "k3s.service"},
		})
		if err != nil || result.ExitCode != 0 {
			lastDetail = "k3s.service is not active"
			continue
		}
		result, err = runner.Run(ctx, host.Command{
			Name: "k3s", Args: []string{"kubectl", "get", "--raw", "/readyz"},
		})
		if err != nil || result.ExitCode != 0 {
			lastDetail = "the kubernetes api is not ready yet"
			continue
		}
		progress.Done("")
		break
	}
	return waitNodeReady(ctx, runner, nodeName, capabilities, progress)
}

// datastoreIsEtcd reports whether this server runs on embedded etcd.
// Legacy installs predate cluster-init and run on the default sqlite
// datastore, which cannot accept additional servers.
func datastoreIsEtcd(ctx context.Context, runner host.Runner) bool {
	info, err := runner.Stat(ctx, k3sEtcdDBDir)
	return err == nil && info.Exists
}

// waitNodeReady polls the named node through k3s's own kubectl (via the
// runner, so the phase stays portable to remote runners) until it reports
// Ready and carries the stamped capability labels.
func waitNodeReady(ctx context.Context, runner host.Runner, nodeName string, capabilities []string, progress Progress) error {
	progress.Start("Stamp capability labels on node")
	deadline := time.Now().Add(5 * time.Minute)
	var lastErr error
	for {
		if time.Now().After(deadline) {
			if lastErr != nil {
				return fmt.Errorf("k3s node never became ready: %w", lastErr)
			}
			return fmt.Errorf("k3s node never became ready")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
		result, err := runner.Run(ctx, host.Command{
			Name: "k3s", Args: []string{"kubectl", "get", "nodes", "-o", "json"},
		})
		if err != nil || result.ExitCode != 0 {
			lastErr = fmt.Errorf("k3s kubectl get nodes: exit %d", result.ExitCode)
			continue
		}
		ready, labels, err := parseNodeList([]byte(result.Stdout), nodeName)
		if err != nil {
			lastErr = err
			continue
		}
		if !ready {
			lastErr = fmt.Errorf("node %s is not ready yet", nodeName)
			continue
		}
		missing := missingCapabilityLabels(labels, capabilities)
		if len(missing) > 0 {
			lastErr = fmt.Errorf("node %s is missing labels %s", nodeName, strings.Join(missing, ", "))
			continue
		}
		progress.Done(nodeName)
		return nil
	}
}

// parseNodeList extracts the named node's readiness and labels from a
// kubectl node list; on multi-node clusters the list carries every member.
func parseNodeList(data []byte, nodeName string) (ready bool, labels map[string]string, err error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Status struct {
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return false, nil, fmt.Errorf("parse node list: %w", err)
	}
	for _, node := range list.Items {
		if node.Metadata.Name != nodeName {
			continue
		}
		for _, condition := range node.Status.Conditions {
			if condition.Type == "Ready" && condition.Status == "True" {
				ready = true
			}
		}
		return ready, node.Metadata.Labels, nil
	}
	return false, nil, fmt.Errorf("node %s is not registered yet", nodeName)
}

func missingCapabilityLabels(labels map[string]string, capabilities []string) []string {
	var missing []string
	for key, value := range layout.CapabilityLabels(capabilities) {
		if labels[key] != value {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	return missing
}

// uninstallK3s runs the role-appropriate uninstall script the install
// script generated.
func uninstallK3s(ctx context.Context, runner host.Runner, role string) error {
	script := k3sUninstallScript
	if role == layout.RoleAgent {
		script = k3sAgentUninstallScript
	}
	result, err := runner.Run(ctx, host.Command{Name: script})
	if err != nil {
		return fmt.Errorf("run %s: %w", script, err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("%s failed with exit code %d: %s", script, result.ExitCode,
			strings.TrimSpace(result.Stderr))
	}
	return nil
}
