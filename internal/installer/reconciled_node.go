package installer

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/layout"
)

// ApplyNodeCapabilities updates both the persistent k3s configuration and
// the live Kubernetes Node labels. Existing unrelated k3s settings and
// labels remain untouched.
func ApplyNodeCapabilities(ctx context.Context, runner host.Runner, record *Record,
	capabilities []string) error {
	if err := PersistNodeCapabilities(ctx, runner, record, capabilities); err != nil {
		return err
	}
	args := []string{"kubectl", "label", "node", record.Node.Name, "--overwrite"}
	desired := layout.CapabilityLabels(capabilities)
	for _, capability := range layout.Capabilities {
		key := layout.CapabilityLabel(capability)
		if value, ok := desired[key]; ok {
			args = append(args, key+"="+value)
		} else {
			args = append(args, key+"-")
		}
	}
	result, err := runner.Run(ctx, host.Command{Name: "k3s", Args: args})
	if err != nil {
		return fmt.Errorf("update node capability labels: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("update node capability labels: exit %d: %s",
			result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return nil
}

// PersistNodeCapabilities updates installer-owned host configuration. Live
// Kubernetes labels are applied centrally after every host has
// acknowledged, so this function also works on agent nodes without an
// administrative kubeconfig.
func PersistNodeCapabilities(ctx context.Context, runner host.Runner, record *Record,
	capabilities []string) error {
	if record == nil || !record.Reconciled() {
		return errors.New("capability reconciliation requires a version-2 record")
	}
	capabilities = append([]string(nil), capabilities...)
	sort.Strings(capabilities)
	for _, capability := range capabilities {
		if !slices.Contains(layout.Capabilities, capability) {
			return fmt.Errorf("unknown capability %q", capability)
		}
	}
	data, err := runner.ReadFile(ctx, K3sConfigPath)
	if err != nil {
		return fmt.Errorf("read k3s config: %w", err)
	}
	var config map[string]any
	if err := yaml.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("parse k3s config: %w", err)
	}
	var labels []string
	switch value := config["node-label"].(type) {
	case []any:
		for _, item := range value {
			if label, ok := item.(string); ok && !strings.HasPrefix(label, layout.CapabilityLabelPrefix) {
				labels = append(labels, label)
			}
		}
	case []string:
		for _, label := range value {
			if !strings.HasPrefix(label, layout.CapabilityLabelPrefix) {
				labels = append(labels, label)
			}
		}
	}
	for key, value := range layout.CapabilityLabels(capabilities) {
		labels = append(labels, key+"="+value)
	}
	sort.Strings(labels)
	config["node-label"] = labels
	rendered, err := yaml.Marshal(config)
	if err != nil {
		return err
	}
	if err := runner.ReplaceFile(ctx, K3sConfigPath, K3sConfigPath+".prev",
		rendered, 0o600); err != nil {
		return err
	}

	record.Node.Capabilities = capabilities
	return SaveRecord(ctx, runner, record)
}

// DecommissionK3s removes Kubernetes membership and k3s while retaining the
// coordinator identity long enough for the agent to acknowledge completion.
func DecommissionK3s(ctx context.Context, runner host.Runner, record *Record) error {
	if record == nil || !record.Reconciled() {
		return errors.New("node decommission requires a version-2 record")
	}
	if record.Lifecycle == nil {
		record.Lifecycle = &InstallLifecycle{}
	}
	record.Lifecycle.Status = InstallStatusRemoving
	record.Lifecycle.Phase = InstallPhaseDraining
	record.Lifecycle.LastError = ""
	record.Lifecycle.UpdatedAt = time.Now().UTC().Truncate(time.Second)
	if err := SaveRecord(ctx, runner, record); err != nil {
		return err
	}
	if _, err := PlanNodeRemoval(ctx, runner, record); err != nil {
		return err
	}
	// In reconciled mode the leader has already drained and deleted the
	// Kubernetes Node before authorizing this typed host action. Repeating
	// the legacy self-removal here can fail after that successful deletion
	// and, for agents, was impossible because no admin kubeconfig exists.
	script := k3sUninstallScript
	if record.Node.Role == layout.RoleAgent {
		script = k3sAgentUninstallScript
	}
	info, err := runner.Stat(ctx, script)
	if err != nil {
		return err
	}
	if info.Exists {
		if err := uninstallK3s(ctx, runner, record.Node.Role); err != nil {
			return err
		}
	} else if record.RegistrationMayHaveStarted() {
		if err := installK3sFiles(ctx, runner, k3sNode{Role: record.Node.Role}, silentProgress{}); err != nil {
			return err
		}
		if err := uninstallK3s(ctx, runner, record.Node.Role); err != nil {
			return err
		}
	} else if err := runner.Remove(ctx, K3sConfigDir); err != nil {
		return err
	}
	record.Lifecycle.Status = InstallStatusRemoving
	record.Lifecycle.Phase = InstallPhaseUninstalling
	record.Lifecycle.UpdatedAt = time.Now().UTC().Truncate(time.Second)
	return SaveRecord(ctx, runner, record)
}

// ScheduleHostdSelfRemoval lets the current agent acknowledge decommission
// before a transient systemd unit removes the agent itself.
func ScheduleHostdSelfRemoval(ctx context.Context, runner host.Runner) error {
	command := strings.Join([]string{
		"systemctl disable --now " + HostdCoordinatorUnit + " " + HostdAgentUnit,
		"rm -f " + HostdCoordinatorPath + " " + HostdAgentUnitPath,
		"rm -rf " + StateDir,
		"rm -f " + HostdBinaryPath,
		"systemctl daemon-reload",
	}, "; ")
	result, err := runner.Run(ctx, host.Command{
		Name: "systemd-run",
		Args: []string{"--unit=skali-hostd-cleanup", "--on-active=2s",
			"/bin/sh", "-c", command},
	})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("schedule hostd cleanup: exit %d: %s",
			result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return nil
}
