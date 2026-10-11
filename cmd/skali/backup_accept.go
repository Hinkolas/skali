package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
)

func newBackupAcceptCommand() *cobra.Command {
	var (
		environment string
		remote      string
		yes         bool
	)
	command := &cobra.Command{
		Use:   "accept",
		Short: "Accept the data an incomplete restore left, so the environment can deploy again",
		Long: "A restore that stops an environment and does not complete holds it:\n" +
			"the environment stays down and deployments are refused, because its\n" +
			"data may be partial. Re-running a restore (skali backup restore) is\n" +
			"the way back to complete data. When that is not possible, accept\n" +
			"the data as it is: components the restore rewrote may hold partial\n" +
			"data, and the others still hold what they held before.\n\n" +
			"Accepting lifts the hold only; the environment stays down until the\n" +
			"next deploy. The restore is shown and confirmed by typing the\n" +
			"environment name; --yes skips that.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			ctx := command.Context()
			out := command.OutOrStdout()
			style := clirender.StyleFor(out)
			in := bufio.NewReader(command.InOrStdin())
			start, err := os.Getwd()
			if err != nil {
				return err
			}
			target, err := resolveQueryTarget(ctx, start, environment, remote)
			if err != nil {
				return err
			}
			status, err := target.api.EnvironmentStatus(ctx, target.environmentID)
			if err != nil {
				return err
			}
			held := status.IncompleteRestore
			if held == nil {
				return fmt.Errorf("no incomplete restore holds environment %s", target.environment)
			}
			if held.Status == "running" {
				return fmt.Errorf("restore %s into %s is still running; wait for it, or cancel its run first",
					held.ID, target.environment)
			}
			printHeader(out, style,
				headerRow{"remote", target.remoteName, target.master},
				headerRow{"project", target.project, ""},
				headerRow{"environment", target.environment, style.Yellow("down, held by an incomplete restore")},
				headerRow{"restore", held.ID, incompleteRestoreDetail(held)})
			if !yes {
				if !cliprompt.Interactive() {
					return errors.New("non-interactive use requires --yes")
				}
				confirmed, err := promptSession(out, in).ConfirmTyped(ctx,
					fmt.Sprintf("Accept the data restore %s left in %s?", held.ID, target.environment),
					acceptWarning(held),
					target.environment)
				if err != nil {
					return err
				}
				if !confirmed {
					return errors.New("aborted")
				}
			}
			runID, err := target.api.AcceptIncompleteRestore(ctx, target.environmentID, held.ID)
			if isReauthRequired(err) {
				if err = reauthSession(ctx, out, in, target.api); err != nil {
					return err
				}
				runID, err = target.api.AcceptIncompleteRestore(ctx, target.environmentID, held.ID)
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "%s%s  run %s\n", style.Check(), style.Bold(style.Green("restore hold lifted")), runID)
			fmt.Fprintf(out, "%s stays down until the next deploy brings it back on its current data\n",
				target.environment)
			return nil
		},
	}
	command.Flags().StringVar(&environment, "environment", "", "environment the restore holds; defaults to the checkout binding")
	command.Flags().StringVar(&remote, "remote", "",
		"remote to target for this one invocation, ignoring the checkout binding and the current remote")
	command.Flags().BoolVar(&yes, "yes", false, "skip the typed confirmation")
	return command
}

// incompleteRestoreDetail summarizes a held restore for a header row.
func incompleteRestoreDetail(held *client.IncompleteRestore) string {
	detail := fmt.Sprintf("snapshot %s, %s %s", held.SnapshotID, held.Status,
		held.StartedAt.Local().Format("2006-01-02 15:04"))
	if held.Error != "" {
		detail += ": " + held.Error
	}
	return detail
}

// acceptWarning spells out what accepting a held restore's data means.
func acceptWarning(held *client.IncompleteRestore) string {
	rewritten := "the components it rewrote"
	if len(held.Components) > 0 {
		rewritten = strings.Join(held.Components, ", ")
	}
	return "The restore did not complete. " + rewritten + " may hold partial data; " +
		"components it never reached still hold their data from before. " +
		"Deploys are allowed again, and the next one brings the environment back on this data."
}

// restoreHoldError explains a restore run that ended without completing:
// when it stopped the environment, the hold it left and both ways
// forward; otherwise the environment was never touched.
func restoreHoldError(status *client.EnvironmentStatus, runID, environment string, cause error) error {
	held := status.IncompleteRestore
	if held == nil || held.RunID == nil || *held.RunID != runID {
		return cause
	}
	return fmt.Errorf("%w\n%s stays down and deploys are refused until a restore completes: "+
		"re-run the restore, or accept the data as it is with: skali backup accept --environment %s",
		cause, environment, environment)
}
