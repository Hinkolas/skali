package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/clirender"
)

// newValuesCommand lists an environment's stored values. Values are
// write-only: the listing shows names and versions only, and there is no
// read-back. Deployments are the only writer (--env-file / .env staging)
// and the only remover (--prune-values), so there is nothing else to do
// here.
func newValuesCommand() *cobra.Command {
	var environment, remote string
	command := &cobra.Command{
		Use:   "values",
		Short: "List the environment's stored values by name and version",
		Long: "Lists the stored values of a remote environment by name and version;\n" +
			"contents are never shown. Values are staged by deployments\n" +
			"(--env-file or a discovered .env) and removed by deploying with\n" +
			"--prune-values. For the local platform use skali dev values.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			ctx := command.Context()
			start, err := os.Getwd()
			if err != nil {
				return err
			}
			target, err := resolveQueryTarget(ctx, start, environment, remote)
			if err != nil {
				return err
			}
			return printStoredValues(ctx, command.OutOrStdout(), target.api, target.environmentID)
		},
	}
	command.Flags().StringVar(&environment, "environment", "", "environment to list; defaults to the checkout binding")
	command.Flags().StringVar(&remote, "remote", "",
		"remote to target for this one invocation, ignoring the checkout binding and the current remote")
	return command
}

// newDevValuesCommand is the local twin of skali values: the current
// project's local environment, like dev logs and dev exec.
func newDevValuesCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "values",
		Short: "List the local project's stored values by name and version",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			api, environmentID, err := localProjectEnvironment(command)
			if err != nil {
				return err
			}
			return printStoredValues(command.Context(), command.OutOrStdout(), api, environmentID)
		},
	}
}

func printStoredValues(ctx context.Context, out io.Writer, api *client.Client, environmentID string) error {
	entries, err := api.EnvironmentValues(ctx, environmentID)
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
}
