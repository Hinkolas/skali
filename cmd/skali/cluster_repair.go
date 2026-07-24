package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/clusterstate"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/layout"
	versionpkg "github.com/Hinkolas/skali/internal/version"
)

func newClusterRepairCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "repair",
		Short: "Diagnose and repair this installation with scoped, confirmed actions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := os.Stdout
			if existingClusterMode() {
				return errors.New("existing-cluster mode has no host to repair; reconverge the bundle with " +
					"skali cluster upgrade --mode existing-cluster, and diagnose with skali cluster diagnose")
			}
			banner(out)
			reader := bufio.NewReader(os.Stdin)
			return runRepairFlow(cmd.Context(), out, reader, yes)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm every planned repair action")
	return cmd
}

// runRepairFlow is diagnose-first: run the read-only diagnosis, print it,
// derive the scoped actions, confirm each individually, act, and
// re-diagnose. Both the command and the menu's repair entry land here. A
// clean diagnosis performs no mutation (section 6 contract).
func runRepairFlow(ctx context.Context, out *os.File, reader *bufio.Reader, yes bool) error {
	if _, err := darwinPrelude(ctx, out, vmPolicyMaintain, ""); err != nil {
		return err
	}
	status, err := installer.GatherStatus(ctx, runner())
	if err != nil {
		return err
	}
	if status.Reconciled != nil && status.Reconciled.ReconciliationPaused {
		if !yes {
			if !cliprompt.Interactive() {
				return errors.New("coordinator reconciliation is paused; repair requires --yes to resume it")
			}
			if !cliprompt.Confirm(reader,
				"Resume coordinator reconciliation from its durable target? [y/N] ") {
				return errors.New("coordinator reconciliation remains paused")
			}
		}
		store, _, err := reconciledClusterStore(ctx)
		if err != nil {
			return err
		}
		if _, err := store.Update(ctx, func(state *clusterstate.State) error {
			state.ReconciliationPaused = false
			return nil
		}); err != nil {
			return err
		}
		fmt.Fprintln(out, "coordinator reconciliation resumed")
		status, err = installer.GatherStatus(ctx, runner())
		if err != nil {
			return err
		}
	}
	detected := status.Host
	if detected.State == installer.StateInterrupted && detected.Record != nil &&
		detected.Record.Reconciled() && detected.Record.Node.Role == layout.RoleServer &&
		detected.Record.Join == nil {
		if !yes {
			if !cliprompt.Interactive() {
				return errors.New("repairing the interrupted seed coordinator requires --yes")
			}
			if !cliprompt.Confirm(reader,
				"Resume the seed coordinator bootstrap with the same cluster identity? [y/N] ") {
				return errors.New("seed coordinator repair was not confirmed")
			}
		}
		hostdBinary, _, err := loadHostdBinary()
		if err != nil {
			return err
		}
		if err := bootstrapReconciledSeed(ctx, detected.Record, hostdBinary); err != nil {
			return err
		}
		fmt.Fprintln(out, "seed coordinator bootstrap repaired")
		status, err = installer.GatherStatus(ctx, runner())
		if err != nil {
			return err
		}
		detected = status.Host
	}
	if detected.State == installer.StateOrphaned {
		if !yes {
			if !cliprompt.Interactive() {
				return errors.New("recovering an orphaned Skali install non-interactively requires --yes")
			}
			if !cliprompt.Confirm(reader,
				"Recover ownership of this fingerprinted interrupted Skali install? [y/N] ") {
				return errors.New("recovery was not confirmed; nothing was changed")
			}
		}
		if err := installer.PersistOrphanRecord(ctx, runner(), detected.Record); err != nil {
			return err
		}
		status, err = installer.GatherStatus(ctx, runner())
		if err != nil {
			return err
		}
		detected = status.Host
	}
	switch detected.State {
	case installer.StateServer, installer.StateAgent, installer.StateDamaged,
		installer.StateInterrupted, installer.StateEnrolled:
	case installer.StateUnmanaged:
		return unmanagedError()
	default:
		return fmt.Errorf("nothing to repair on this host (state %s); run skali cluster install first",
			detected.State)
	}
	// An interrupted converge left the version in place but no stamp; a
	// version mismatch instead belongs to upgrade. Only trustworthy when
	// the API answered, else a plain k3s outage would look like one.
	stampMissing := status.ClusterReachable && status.Initialized && !status.BundleCurrent &&
		status.BundleVersion == versionpkg.Version

	diagnosis, err := installer.Diagnose(ctx, runner(), installer.DiagnoseOptions{})
	if err != nil {
		return err
	}
	printDiagnosis(out, diagnosis)
	if diagnosis.Fails() == 0 && !stampMissing {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "nothing to repair")
		return nil
	}
	if stampMissing {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "the last converge was interrupted (the bundle hash stamp is missing)")
	}

	tasks := clirender.NewTasks(out)
	progress := newTaskProgress(tasks)
	var hostdBinary []byte
	if detected.Record != nil && detected.Record.Reconciled() {
		hostdBinary, _, _ = loadHostdBinary()
	}
	actions, refusals := installer.PlanRepairs(diagnosis, installer.RepairDeps{
		Runner:       runner(),
		Record:       detected.Record,
		HostdBinary:  hostdBinary,
		StampMissing: stampMissing,
		Progress:     progress,
	})
	fmt.Fprintln(out)
	for _, refusal := range refusals {
		fmt.Fprintf(out, "refused: %s\n", refusal)
	}
	if len(actions) == 0 {
		if remaining := diagnosis.Fails(); remaining > 0 {
			return fmt.Errorf("no automatic repair applies; %d problem(s) remain", remaining)
		}
		fmt.Fprintln(out, "nothing to repair")
		return nil
	}

	for _, action := range actions {
		if !yes {
			if !cliprompt.Interactive() {
				return errors.New("non-interactive run requires --yes")
			}
			if !cliprompt.Confirm(reader, action.Confirm) {
				fmt.Fprintf(out, "skipped: %s\n", action.Title)
				continue
			}
		}
		if err := action.Run(ctx); err != nil {
			progress.Abort()
			return fmt.Errorf("%s: %w", strings.ToLower(action.Title[:1])+action.Title[1:], err)
		}
	}
	progress.Done("")

	fmt.Fprintln(out)
	final, err := installer.Diagnose(ctx, runner(), installer.DiagnoseOptions{})
	if err != nil {
		return err
	}
	printDiagnosis(out, final)
	if fails := final.Fails(); fails > 0 {
		return fmt.Errorf("%d problem(s) remain after repair", fails)
	}
	return nil
}
