package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/installer"
)

// newClusterRestoreCmd is the planned disaster-recovery command: rebuild a
// Skali installation on a fresh host from the three inputs that nothing in
// a lost cluster can recreate: the saved installation record
// (installer.RecordPath), an off-cluster backup of the system database,
// and the registry artifacts (release images and cache). It should
// reinstall k3s and the bundle from the record, restore the database
// before skalid starts, repopulate the registry, and finish with the same
// ready summary as init. Hidden until it exists: today it only prints that
// contract and fails, which is worse than absent in the disaster it is
// meant for. Tracked in ROADMAP.md under "Documented recovery runbook".
func newClusterRestoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "restore",
		Short:  "Restore a Skali installation from saved inputs",
		Hidden: true,
		Args:   cobra.NoArgs,
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
			return fmt.Errorf("restore is not available yet; follow the recovery runbook in the meantime")
		},
	}
}
