package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
)

// newValuesCommand groups the stored-value inspection and removal surface.
// Values are write-only: ls shows names and versions, unset removes; there
// is no read-back.
func newValuesCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "values",
		Short: "Inspect and remove stored environment values",
	}
	command.AddCommand(newValuesLsCommand(), newValuesUnsetCommand())
	return command
}

func newValuesLsCommand() *cobra.Command {
	var environment, remote string
	command := &cobra.Command{
		Use:   "ls",
		Short: "List the environment's stored values by name and version",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			ctx := command.Context()
			out := command.OutOrStdout()
			start, err := os.Getwd()
			if err != nil {
				return err
			}
			target, err := resolveQueryTarget(ctx, start, environment, remote)
			if err != nil {
				return err
			}
			entries, err := target.api.EnvironmentValues(ctx, target.environmentID)
			if err != nil {
				return err
			}
			style := clirender.StyleFor(out)
			if len(entries) == 0 {
				fmt.Fprintln(out, style.Dim("no stored values"))
				return nil
			}
			for _, entry := range entries {
				fmt.Fprintf(out, "%s  %s\n", entry.Name, style.Dim(fmt.Sprintf("v%d", entry.Version)))
			}
			return nil
		},
	}
	command.Flags().StringVar(&environment, "environment", "", "environment to list; defaults to the checkout binding")
	command.Flags().StringVar(&remote, "remote", "",
		"remote to target for this one invocation, ignoring the checkout binding and the current remote")
	return command
}

func newValuesUnsetCommand() *cobra.Command {
	var (
		environment string
		remote      string
		yes         bool
	)
	command := &cobra.Command{
		Use:   "unset NAME",
		Short: "Remove one stored environment value",
		Long: "Removes the stored value immediately. Running revisions keep the\n" +
			"pinned versions they resolved, and the next deployment no longer\n" +
			"includes the name.",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			ctx := command.Context()
			out := command.OutOrStdout()
			name := args[0]
			start, err := os.Getwd()
			if err != nil {
				return err
			}
			target, err := resolveQueryTarget(ctx, start, environment, remote)
			if err != nil {
				return err
			}
			if !yes {
				if !cliprompt.Interactive() {
					return fmt.Errorf("non-interactive use requires --yes")
				}
				confirmed, err := cliprompt.New(os.Stdin, out).Confirm(ctx, cliprompt.ConfirmOptions{
					Title: fmt.Sprintf("Unset %s in %s?", name, target.environment),
					Description: "The stored value is removed; running revisions keep their " +
						"pinned versions, and the next deployment no longer includes it.",
				})
				if err != nil {
					return err
				}
				if !confirmed {
					return fmt.Errorf("aborted")
				}
			}
			if err := target.api.UnsetEnvironmentValue(ctx, target.environmentID, name); err != nil {
				return err
			}
			fmt.Fprintf(out, "unset %s in %s\n", name, target.environment)
			return nil
		},
	}
	command.Flags().StringVar(&environment, "environment", "", "environment to modify; defaults to the checkout binding")
	command.Flags().StringVar(&remote, "remote", "",
		"remote to target for this one invocation, ignoring the checkout binding and the current remote")
	command.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return command
}
