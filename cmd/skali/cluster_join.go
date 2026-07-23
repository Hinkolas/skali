package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/installer/limavm"
	"github.com/Hinkolas/skali/internal/layout"
)

func newClusterJoinCmd() *cobra.Command {
	var server, tokenFile, role, cluster, nodeIP string
	var capabilities []string
	var assumeYes bool
	cmd := &cobra.Command{
		Use:   "join",
		Short: "Join this host to an existing cluster",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := os.Stdout

			if existingClusterMode() {
				return existingModeRefusal("join")
			}
			if role != "" && role != layout.RoleAgent && role != layout.RoleServer {
				return fmt.Errorf("role must be server or agent, got %q", role)
			}
			// The full flag set is the consent, like --config elsewhere;
			// join never prompts. Interactive enrollment lives in the
			// fresh-host flow of a bare run.
			if tokenFile == "" || len(capabilities) == 0 {
				return fmt.Errorf("join requires --token-file and --capabilities; current Skali tokens supply --server, --role, and --cluster")
			}

			if _, err := darwinPrelude(ctx, out, vmPolicyInstall, ""); err != nil {
				return err
			}
			// Join creates the VM with the fleet defaults (bridged), so
			// dependency provisioning targets that network. It confirms
			// (or takes --yes) and runs before the task printer starts,
			// because sudo may prompt.
			if err := ensureDarwinDeps(ctx, out, nil, limavm.NetworkBridged, assumeYes); err != nil {
				return err
			}
			tasks := clirender.NewTasks(out)
			progress := newTaskProgress(tasks)
			// On a Mac without a VM, join creates one with the fleet
			// defaults; sizing overrides go through install --config.
			if err := ensureDarwinVM(ctx, progress, nil); err != nil {
				progress.Abort()
				return err
			}
			opts := installer.InstallOptions{
				Cluster:       cluster,
				Role:          role,
				Capabilities:  capabilities,
				Join:          &installer.JoinOptions{Server: server, TokenFile: tokenFile},
				NodeIP:        nodeIP,
				Progress:      progress,
				RecoverOrphan: true,
			}
			if err := applyDarwinInstallOptions(ctx, &opts); err != nil {
				progress.Abort()
				return err
			}
			_, err := installer.Install(ctx, runner(), opts)
			if err != nil {
				progress.Abort()
				return err
			}
			warnings := finishDarwinInstall(ctx, progress)
			progress.Done("")
			warnings = append(warnings, serverCountWarning(ctx, role)...)
			printWarnings(out, warnings)
			return nil
		},
	}
	cmd.Flags().StringVar(&server, "server", "", "alternate URL of an existing k3s server (current Skali tokens supply a default)")
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "path to a file holding the join token")
	cmd.Flags().StringVar(&role, "role", "", "k3s role of this host; must match the token claim")
	cmd.Flags().StringSliceVar(&capabilities, "capabilities", nil, "designated workload capabilities for this node")
	cmd.Flags().StringVar(&cluster, "cluster", "", "name of the cluster being joined; must match the token claim")
	cmd.Flags().StringVar(&nodeIP, "node-ip", "", "IP address this node advertises inside the cluster (multi-homed hosts)")
	cmd.Flags().BoolVar(&assumeYes, "yes", false, "provision missing Mac dependencies without confirmation (macOS only)")
	return cmd
}

// serverCountWarning returns the quorum note after a server join left the
// cluster at an even server count; agents and unknown counts stay silent
// (the join itself succeeded, the count is advice).
func serverCountWarning(ctx context.Context, role string) []string {
	if role != layout.RoleServer {
		return nil
	}
	servers, err := installer.CountServers(ctx, runner())
	if err != nil || servers == 0 || servers%2 == 1 {
		return nil
	}
	return []string{fmt.Sprintf("the cluster now has %d servers; etcd quorum prefers one or three, "+
		"so join another server soon", servers)}
}
