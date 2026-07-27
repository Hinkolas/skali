package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/installer/limavm"
)

func newClusterInstallCmd() *cobra.Command {
	return newClusterInstallCommand("install",
		"Install k3s and prepare this host as a Skali node", false)
}

func newClusterCreateCmd() *cobra.Command {
	return newClusterInstallCommand("create",
		"Create a reconciled cluster seed and coordinator", true)
}

func newClusterInstallCommand(use, short string, seedOnly bool) *cobra.Command {
	var configPath string
	var assumeYes bool
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := os.Stdout

			if configPath == "" {
				if !cliprompt.Interactive() {
					return fmt.Errorf("non-interactive run requires --config")
				}
				banner(out)
				present, err := darwinPrelude(ctx, out, vmPolicyInstall, "")
				if err != nil {
					return err
				}
				if !present {
					printDarwinFreshHeader(out)
					if seedOnly {
						return runInteractiveCreateFlow(ctx, out)
					}
					return runInteractiveFreshFlow(ctx, out)
				}
				detected, err := installer.Detect(ctx, runner())
				if err != nil {
					return err
				}
				switch detected.State {
				case installer.StateFresh:
					printFreshHeader(out, detected)
					if seedOnly {
						return runInteractiveCreateFlow(ctx, out)
					}
					return runInteractiveFreshFlow(ctx, out)
				case installer.StateInterrupted, installer.StateOrphaned:
					status, err := installer.GatherStatus(ctx, runner())
					if err != nil {
						return err
					}
					printStatus(out, status)
					return runRecoveryMenu(ctx, out, status)
				default:
					return fmt.Errorf("this host is not fresh (state %s); "+
						"run skali cluster without arguments for maintenance options", detected.State)
				}
			}

			// A config file is full consent: every decision comes from it
			// and nothing prompts.
			data, err := os.ReadFile(configPath)
			if err != nil {
				return fmt.Errorf("read %s: %w", configPath, err)
			}
			config, err := installer.ParseNodeConfig(data)
			if err != nil {
				return err
			}
			if seedOnly && config.Join != nil {
				return fmt.Errorf("cluster create accepts only a seed-server config; use cluster join for enrollment")
			}
			if config.VM != nil && runtime.GOOS != "darwin" {
				return fmt.Errorf("node config: the vm block applies only to macOS hosts")
			}
			configVMName := ""
			if config.VM != nil {
				configVMName = config.VM.Name
			}
			if _, err := darwinPrelude(ctx, out, vmPolicyInstall, configVMName); err != nil {
				return err
			}
			// Mac dependency provisioning is the one thing outside the
			// config's consent: it confirms (or takes --yes) and must run
			// before the task printer starts, because sudo may prompt.
			network := limavm.NetworkBridged
			if config.VM != nil && config.VM.Network != "" {
				network = limavm.Network(config.VM.Network)
			}
			if err := ensureDarwinDeps(ctx, out, nil, network, assumeYes); err != nil {
				return err
			}
			tasks := clirender.NewTasks(out)
			progress := newTaskProgress(tasks)
			if err := ensureDarwinVM(ctx, progress, config.VM); err != nil {
				progress.Abort()
				return err
			}
			opts := installer.InstallOptions{
				Cluster:      config.Cluster,
				Role:         config.Role,
				Capabilities: config.Capabilities,
				Network:      config.NodeNetwork(),
				Progress:     progress,
				// A matching config file is full non-interactive consent
				// to recover the exact Skali fingerprint it describes.
				RecoverOrphan: true,
			}
			if config.Join != nil {
				tokenData, readErr := readHostFile(ctx, config.Join.TokenFile)
				if readErr != nil {
					progress.Abort()
					return fmt.Errorf("read join token file %s: %w", config.Join.TokenFile, readErr)
				}
				rawToken := strings.TrimSpace(string(tokenData))
				if reconciledToken(rawToken) {
					record, enrollErr := runReconciledEnrollment(ctx, reconciledEnrollmentOptions{
						Server: config.Join.Server, Token: rawToken,
						Capabilities: config.Capabilities, Network: config.NodeNetwork(),
						RequestedRole: config.Role, RequestedCluster: config.Cluster,
					})
					if enrollErr != nil {
						progress.Abort()
						return enrollErr
					}
					progress.Done("")
					fmt.Fprintf(out, "node %s enrolled in cluster %q; pending cluster apply\n",
						record.Node.Name, record.Cluster)
					return nil
				}
				opts.Join = &installer.JoinOptions{
					Server:    config.Join.Server,
					TokenFile: config.Join.TokenFile,
				}
			}
			var hostdBinary []byte
			if config.Join == nil {
				opts.Management = installer.ManagementReconciled
				hostdBinary, _, err = loadHostdBinary()
				if err != nil {
					progress.Abort()
					return err
				}
			}
			if err := applyDarwinInstallOptions(ctx, &opts); err != nil {
				progress.Abort()
				return err
			}
			record, err := installer.Install(ctx, runner(), opts)
			if err != nil {
				progress.Abort()
				return err
			}
			if config.Join == nil {
				progress.Start("Bootstrap cluster coordinator")
				if err := bootstrapReconciledSeed(ctx, record, hostdBinary); err != nil {
					progress.Abort()
					return err
				}
				progress.Done("")
			}
			warnings := finishDarwinInstall(ctx, progress)
			progress.Done("")
			printWarnings(out, warnings)
			return nil
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "non-interactive node configuration (node.yaml)")
	cmd.Flags().BoolVar(&assumeYes, "yes", false, "provision missing Mac dependencies without confirmation (macOS only)")
	return cmd
}
