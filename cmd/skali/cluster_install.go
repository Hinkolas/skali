package main

import (
	"bufio"
	"fmt"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/installer/limavm"
)

func newClusterInstallCmd() *cobra.Command {
	var configPath string
	var assumeYes bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install k3s and prepare this host as a Skali node",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := os.Stdout

			if existingClusterMode() {
				return runExistingInstall(ctx, out, bufio.NewReader(os.Stdin), configPath)
			}

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
					return runInteractiveFreshFlow(ctx, out)
				}
				detected, err := installer.Detect(ctx, runner())
				if err != nil {
					return err
				}
				switch detected.State {
				case installer.StateFresh:
					printFreshHeader(out, detected)
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
				NodeIP:       config.NodeIP,
				Progress:     progress,
				// A matching config file is full non-interactive consent
				// to recover the exact Skali fingerprint it describes.
				RecoverOrphan: true,
			}
			if config.Join != nil {
				opts.Join = &installer.JoinOptions{
					Server:    config.Join.Server,
					TokenFile: config.Join.TokenFile,
				}
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
			printWarnings(out, warnings)
			return nil
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "non-interactive node configuration (node.yaml)")
	cmd.Flags().BoolVar(&assumeYes, "yes", false, "provision missing Mac dependencies without confirmation (macOS only)")
	return cmd
}
