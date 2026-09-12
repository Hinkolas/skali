package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/installer"
)

func newClusterStatusCommand() *cobra.Command {
	var all bool
	command := &cobra.Command{
		Use:   "status",
		Short: "Show the installation state and health of this host",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			ctx := command.Context()
			out := os.Stdout

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
			printStatus(out, status, all)
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
	command.Flags().BoolVar(&all, "all", false, "include cancelled and removed node history")
	return command
}
