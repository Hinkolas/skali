package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/installer"
)

func newClusterStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the installation state and health of this host",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := os.Stdout

			if existingClusterMode() {
				return runExistingStatus(ctx, out)
			}
			banner(out)

			present, err := darwinPrelude(ctx, out, vmPolicyStatus, "")
			if err != nil {
				return err
			}
			if !present {
				printDarwinFreshHeader(out)
				return nil
			}

			status, err := installer.GatherStatus(ctx, runner())
			if err != nil {
				return err
			}
			switch status.Host.State {
			case installer.StateUnsupported:
				return fmt.Errorf("this host cannot run a skali installation: %s",
					strings.Join(status.Host.Problems, "; "))
			case installer.StateFresh, installer.StateUnmanaged:
				printFreshHeader(out, status.Host)
				return nil
			}
			printStatus(out, status)
			switch status.Host.State {
			case installer.StateDamaged:
				return fmt.Errorf("this installation is damaged")
			case installer.StateInterrupted:
				return fmt.Errorf("this installation is interrupted and can be resumed, repaired, or uninstalled")
			case installer.StateOrphaned:
				return fmt.Errorf("this older interrupted installation requires explicit recovery")
			}
			return nil
		},
	}
}
