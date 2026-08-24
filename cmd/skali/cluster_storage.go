package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/layout"
)

// newClusterStorageMigrateCmd moves the registry volume onto the Longhorn
// storage class on an installation that predates it. Project app volumes
// migrate through the backup/restore runbook instead: those need skalid's
// backup machinery, which this host-side command deliberately has no
// access to.
func newClusterStorageMigrateCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "storage-migrate",
		Short: "Move the registry volume onto the replicated storage class",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := os.Stdout
			banner(out)
			if _, err := darwinPrelude(ctx, out, vmPolicyMaintain, ""); err != nil {
				return err
			}
			detected, err := installer.Detect(ctx, runner())
			if err != nil {
				return err
			}
			if detected.State != installer.StateServer || detected.Record == nil {
				return fmt.Errorf("this host does not carry a complete server installation (state %s)",
					detected.State)
			}
			record := detected.Record
			if record.Node.Role != layout.RoleServer {
				return errors.New("run the migration on the server that maintains the bundle")
			}

			fmt.Fprintln(out, "This migration recreates the registry volume on the "+
				"replicated storage class. The registry's stored images are discarded; "+
				"they are rebuilt lazily as projects deploy. Running deployments keep "+
				"their current images.")
			if !yes {
				if !cliprompt.Interactive() {
					return errors.New("non-interactive run requires --yes")
				}
				reader := bufio.NewReader(os.Stdin)
				confirmed, err := promptSession(out, reader).Confirm(ctx, cliprompt.ConfirmOptions{
					Title: "Recreate the registry volume on the replicated storage class?",
				})
				if err != nil {
					return err
				}
				if !confirmed {
					return errors.New("migration cancelled; nothing was changed")
				}
				fmt.Fprintln(out)
			}

			client, err := installer.KubeClient(ctx, runner())
			if err != nil {
				return err
			}
			tasks := clirender.NewTasks(out)
			progress := newTaskProgress(tasks)
			err = installer.MigrateRegistryStorage(ctx, client, runner(), record, progress)
			if errors.Is(err, installer.ErrRegistryStorageMigrated) {
				progress.Done("")
				fmt.Fprintln(out, "already migrated, nothing to do")
				return nil
			}
			if err != nil {
				progress.Abort()
				return err
			}
			progress.Done("")
			fmt.Fprintln(out)
			fmt.Fprintln(out, "registry volume migrated")
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}
