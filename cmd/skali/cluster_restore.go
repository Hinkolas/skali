package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/installer"
)

func newClusterRestoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restore",
		Short: "Restore a Skali installation from saved inputs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := os.Stdout
			banner(out)
			if _, err := darwinPrelude(ctx, out, vmPolicyStatus, ""); err != nil {
				return err
			}
			// Restore's audience is a rebuilt host, so every state prints
			// the contract; detection only decorates the header.
			detected, err := installer.Detect(ctx, runner())
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "host %s: %s\n", hostLabel(detected), detected.State)
			fmt.Fprintln(out)
			fmt.Fprintln(out, "restore rebuilds a Skali installation from three inputs:")
			fmt.Fprintln(out, "  1. the saved installation record (a copy of "+installer.RecordPath+")")
			fmt.Fprintln(out, "  2. an off-cluster database backup")
			fmt.Fprintln(out, "  3. the registry artifacts (release images and cache contents)")
			fmt.Fprintln(out)
			fmt.Fprintln(out, "Keep the record with your backups; nothing in the cluster can recreate it.")
			fmt.Fprintln(out)
			return fmt.Errorf("not implemented in this slice: restore arrives with a later milestone")
		},
	}
}
