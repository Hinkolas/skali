package main

import (
	"context"
	"fmt"
	"github.com/Hinkolas/skali/internal/cliprompt"
	"os"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/installer/limavm"
	"github.com/Hinkolas/skali/internal/layout"
)

func newClusterJoinCommand() *cobra.Command {
	var server, tokenFile, rawToken, role, cluster, nodeIP string
	var publicIPs, extraSANs, coordinatorBind []string
	var capabilities []string
	var assumeYes bool
	command := &cobra.Command{
		Use:   "join [coordinator]",
		Short: "Join this host to an existing cluster",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			ctx := command.Context()
			out := os.Stdout

			if role != "" && role != layout.RoleAgent && role != layout.RoleServer {
				return fmt.Errorf("role must be server or agent, got %q", role)
			}
			var err error
			rawToken, err = joinTokenInput(ctx, rawToken, tokenFile, command.Flags().Changed("token"), command.Flags().Changed("token-file"), os.Getenv("SKALI_JOIN_TOKEN"), command.InOrStdin())
			if err != nil {
				return err
			}
			if len(args) == 1 {
				if server != "" {
					return fmt.Errorf("supply the coordinator as an argument or --server, not both")
				}
				server = args[0]
			}

			network := installer.NodeNetwork{
				ClusterIP: nodeIP, PublicIPs: publicIPs, ExtraSANs: extraSANs,
				CoordinatorBind: coordinatorBind,
			}
			if err := network.Validate(); err != nil {
				return err
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
			if rawToken == "" || reconciledToken(rawToken) {
				record, enrollErr := runReconciledEnrollment(ctx, reconciledEnrollmentOptions{
					Server: server, Token: rawToken, Capabilities: capabilities,
					Network: network, RequestedRole: role, RequestedCluster: cluster,
					Interactive: cliprompt.Interactive(), Out: out,
				})
				if enrollErr != nil {
					progress.Abort()
					return enrollErr
				}
				progress.Done("")
				if !record.EnrolledOnly() {
					fmt.Fprintf(out, "node %s is already joined to cluster %q as %s\n", record.Node.Name, record.Cluster, record.Node.Role)
					return nil
				}
				fmt.Fprintf(out, "node %s enrolled in cluster %q as %s; pending cluster apply\n",
					record.Node.Name, record.Cluster, record.Node.Role)
				fmt.Fprintln(out, "No k3s files or services were installed.")
				return nil
			}
			opts := installer.InstallOptions{
				Cluster:       cluster,
				Role:          role,
				Capabilities:  capabilities,
				Join:          &installer.JoinOptions{Server: server, Token: rawToken},
				Network:       network,
				Progress:      progress,
				RecoverOrphan: true,
			}
			if err := applyDarwinInstallOptions(ctx, &opts); err != nil {
				progress.Abort()
				return err
			}
			_, err = installer.Install(ctx, runner(), opts)
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
	command.Flags().StringVar(&server, "server", "", "coordinator endpoint; legacy composite tokens may supply their k3s endpoint")
	command.Flags().StringVar(&rawToken, "token", "", "join token (defaults to SKALI_JOIN_TOKEN or saved enrollment)")
	command.Flags().StringVar(&tokenFile, "token-file", "", "path to a join token file, or - for stdin")
	command.Flags().StringVar(&role, "role", "", "expected role; must match the invitation or legacy token")
	command.Flags().StringSliceVar(&capabilities, "capabilities", nil, "designated workload capabilities for this node")
	command.Flags().StringVar(&cluster, "cluster", "", "expected cluster name; required only for raw legacy K10 tokens")
	command.Flags().StringVar(&nodeIP, "node-ip", "", "address other cluster nodes reach this node through; defaults to the coordinator route source when unambiguous")
	command.Flags().StringSliceVar(&publicIPs, "public-ip", nil, "address reachable from outside the cluster network; repeatable")
	command.Flags().StringSliceVar(&extraSANs, "tls-san", nil, "additional name or address for the kubernetes api certificate; repeatable")
	command.Flags().StringSliceVar(&coordinatorBind, "coordinator-bind", nil,
		"scopes the enrollment coordinator listens on: cluster, public, or both (servers only)")
	command.Flags().BoolVar(&assumeYes, "yes", false, "provision missing Mac dependencies without confirmation (macOS only)")
	return command
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
