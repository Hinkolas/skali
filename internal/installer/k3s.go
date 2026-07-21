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
)

// Host paths owned by k3s and its install script.
const (
	K3sConfigDir      = "/etc/rancher/k3s"
	K3sConfigPath     = K3sConfigDir + "/config.yaml"
	K3sRegistriesPath = K3sConfigDir + "/registries.yaml"
	K3sBinaryPath     = "/usr/local/bin/k3s"
	K3sKubeconfigPath = K3sConfigDir + "/k3s.yaml"

	k3sUninstallScript      = "/usr/local/bin/k3s-uninstall.sh"
	k3sAgentUninstallScript = "/usr/local/bin/k3s-agent-uninstall.sh"
	k3sInstallScriptPath    = CacheDir + "/k3s-install.sh"
)

// Vendored https://get.k3s.io; the script owns the systemd unit, selinux
// handling, and the generated uninstall scripts, so provisioning stays
// aligned with upstream behavior. Refreshing it is part of any k3s bump.
//
//go:embed assets/k3s-install.sh
var k3sInstallScript []byte

// k3sConfigYAML renders /etc/rancher/k3s/config.yaml. It is written before
// the install script runs so k3s starts configured: capability labels ride
// node registration, Traefik stays enabled as the edge, and the embedded
// registry mirror (Spegel) keeps already-pulled images available while the
// managed registry is down.
func k3sConfigYAML(nodeName, cluster string, capabilities []string) string {
	var builder strings.Builder
	builder.WriteString("node-name: " + nodeName + "\n")
	builder.WriteString("embedded-registry: true\n")
	builder.WriteString("node-label:\n")
	labels := layout.CapabilityLabels(capabilities)
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		builder.WriteString("  - " + key + "=" + labels[key] + "\n")
	}
	builder.WriteString("  - " + layout.ClusterLabel + "=" + cluster + "\n")
	return builder.String()
}

// k3sRegistriesYAML renders /etc/rancher/k3s/registries.yaml, written
// fully at install time: the wildcard mirror enables Spegel, and the
// managed-registry entry maps the constant internal host onto the
// node-local NodePort so containerd resolves production artifact
// references without DNS.
func k3sRegistriesYAML() string {
	return fmt.Sprintf(`mirrors:
  "*":
  %q:
    endpoint:
      - "http://127.0.0.1:%d"
`, bundle.RegistryInternalHost, bundle.RegistryNodePort)
}

// installK3s writes the k3s configuration and runs the vendored install
// script pinned to K3sVersion. Server role only in this slice; the agent
// variant adds the join environment in a later milestone.
func installK3s(ctx context.Context, runner host.Runner, nodeName, cluster string, capabilities []string, progress Progress) error {
	progress.Start("Install k3s " + K3sVersion + " (server)")
	if err := runner.MkdirAll(ctx, K3sConfigDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", K3sConfigDir, err)
	}
	if err := runner.WriteFile(ctx, K3sConfigPath, []byte(k3sConfigYAML(nodeName, cluster, capabilities)), 0o600); err != nil {
		return fmt.Errorf("write k3s config: %w", err)
	}
	if err := runner.WriteFile(ctx, K3sRegistriesPath, []byte(k3sRegistriesYAML()), 0o600); err != nil {
		return fmt.Errorf("write k3s registries config: %w", err)
	}
	if err := runner.MkdirAll(ctx, CacheDir, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", CacheDir, err)
	}
	if err := runner.WriteFile(ctx, k3sInstallScriptPath, k3sInstallScript, 0o700); err != nil {
		return fmt.Errorf("write k3s install script: %w", err)
	}
	result, err := runner.Run(ctx, host.Command{
		Name: "sh",
		Args: []string{k3sInstallScriptPath},
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

// waitNodeReady polls the node through k3s's own kubectl (via the runner,
// so the phase stays portable to remote runners) until it reports Ready
// and carries the stamped capability labels.
func waitNodeReady(ctx context.Context, runner host.Runner, capabilities []string, progress Progress) error {
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
		name, ready, labels, err := parseNodeList([]byte(result.Stdout))
		if err != nil {
			lastErr = err
			continue
		}
		if !ready {
			lastErr = fmt.Errorf("node %s is not ready yet", name)
			continue
		}
		missing := missingCapabilityLabels(labels, capabilities)
		if len(missing) > 0 {
			lastErr = fmt.Errorf("node %s is missing labels %s", name, strings.Join(missing, ", "))
			continue
		}
		progress.Done(name)
		return nil
	}
}

// parseNodeList extracts the single node's name, readiness, and labels
// from a kubectl node list.
func parseNodeList(data []byte) (name string, ready bool, labels map[string]string, err error) {
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
		return "", false, nil, fmt.Errorf("parse node list: %w", err)
	}
	if len(list.Items) == 0 {
		return "", false, nil, fmt.Errorf("no nodes registered yet")
	}
	node := list.Items[0]
	for _, condition := range node.Status.Conditions {
		if condition.Type == "Ready" && condition.Status == "True" {
			ready = true
		}
	}
	return node.Metadata.Name, ready, node.Metadata.Labels, nil
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
