package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/installer"
)

func newClusterTierCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "tier",
		Short: "Apply the available database availability tier",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := os.Stdout
			if existingClusterMode() {
				return errors.New("existing-cluster mode sets the database tier in its config; " +
					"change it with skali cluster install --mode existing-cluster --config")
			}
			banner(out)
			reader := bufio.NewReader(os.Stdin)
			return runTierFlow(cmd.Context(), out, reader, yes)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

// runTierFlow plans, confirms, and applies a database availability-tier
// change on this node. Both the command and the menu's apply-tier entry
// land here. Tier changes are explicit maintenance actions, never side
// effects of membership changes; this flow is that explicit action.
func runTierFlow(ctx context.Context, out *os.File, reader *bufio.Reader, yes bool) error {
	if _, err := darwinPrelude(ctx, out, vmPolicyMaintain, ""); err != nil {
		return err
	}
	status, err := installer.GatherStatus(ctx, runner())
	if err != nil {
		return err
	}
	detected := status.Host
	switch detected.State {
	case installer.StateServer:
	case installer.StateAgent:
		return errors.New("tier changes run on a server node")
	case installer.StateUnmanaged:
		return unmanagedError()
	case installer.StateDamaged:
		return fmt.Errorf("this installation is damaged: %s; run skali cluster diagnose",
			strings.Join(detected.Problems, "; "))
	default:
		return fmt.Errorf("no skali installation on this host (state %s); run skali cluster install first",
			detected.State)
	}
	record := detected.Record

	plan, err := installer.PlanTier(status)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "tier plan for host %s (cluster %q)\n", hostLabel(detected), record.Cluster)
	printTierPlan(out, plan)
	if plan.Nothing() {
		fmt.Fprintf(out, "bootstrap database tier: %s (matches the available tier), nothing to do\n",
			plan.Deployed)
		return nil
	}

	fmt.Fprintln(out)
	if plan.Downgrade {
		fmt.Fprintf(out, "Downgrade the bootstrap database from %s to %s replication. This removes\n"+
			"a replica and lowers availability; no data is deleted.", plan.Deployed, plan.Available)
	} else {
		fmt.Fprintf(out, "Upgrade the bootstrap database from %s to %s replication. This adds\n"+
			"a replica and briefly reconfigures replication; no data is deleted.", plan.Deployed, plan.Available)
	}
	if !yes {
		if !cliprompt.Interactive() {
			fmt.Fprintln(out)
			return errors.New("non-interactive run requires --yes")
		}
		if !cliprompt.Confirm(reader, " Continue? [y/N] ") {
			return errors.New("tier change cancelled; nothing was changed")
		}
	}
	fmt.Fprintln(out)

	tasks := clirender.NewTasks(out)
	progress := newTaskProgress(tasks)
	if err := installer.ApplyTier(ctx, runner(), record, plan, installer.TierOptions{Progress: progress}); err != nil {
		progress.Abort()
		return err
	}
	progress.Done("")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "bootstrap database tier: %s\n", plan.Available)
	if !plan.Downgrade {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Shared platform pools are skalid-owned. Upgrade them through Skali")
		fmt.Fprintln(out, "(System page or the admin CLI); skalid now reports the higher tier as")
		fmt.Fprintln(out, "available for its pools.")
	}
	return nil
}

// printTierPlan renders the drift block the confirmation refers to.
func printTierPlan(out io.Writer, plan installer.TierPlan) {
	fmt.Fprintf(out, "  database nodes    %d (%s)\n", len(plan.DatabaseNodes), strings.Join(plan.DatabaseNodes, ", "))
	fmt.Fprintf(out, "  deployed tier     %s\n", plan.Deployed)
	fmt.Fprintf(out, "  available tier    %s\n", plan.Available)
}
