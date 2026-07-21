package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/installer"
)

func newInstallCmd() *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install k3s and prepare this host as a Skali node",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := os.Stdout

			if configPath == "" {
				if !cliprompt.Interactive() {
					return fmt.Errorf("non-interactive run requires --config")
				}
				banner(out)
				detected, err := installer.Detect(ctx, runner())
				if err != nil {
					return err
				}
				if detected.State != installer.StateFresh {
					return fmt.Errorf("this host is not fresh (state %s); "+
						"run skali-installer without arguments for maintenance options", detected.State)
				}
				printFreshHeader(out, detected)
				return runInteractiveFreshFlow(ctx, out)
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
			tasks := clirender.NewTasks(out)
			progress := newTaskProgress(tasks)
			_, err = installer.Install(ctx, runner(), installer.InstallOptions{
				Cluster:      config.Cluster,
				Capabilities: config.Capabilities,
				Progress:     progress,
			})
			if err != nil {
				progress.Abort()
				return err
			}
			progress.Done("")
			return nil
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "non-interactive node configuration (node.yaml)")
	return cmd
}
