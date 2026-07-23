package main

import (
	"errors"

	"github.com/spf13/cobra"
)

func newPlanCommand() *cobra.Command {
	opts := &deployOptions{UseBinding: true}
	command := &cobra.Command{
		Use:   "plan",
		Short: "Compute and print the deployment plan without changing anything",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			_, err := runDeployFlow(command, opts, true)
			return err
		},
	}
	addDeployFlags(command, opts)
	return command
}

func newDeployCommand() *cobra.Command {
	opts := &deployOptions{UseBinding: true}
	command := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy the project to an environment",
		Long: "Deploys the local manifest: candidate definition, staged values, plan\n" +
			"confirmation, local builds and imports pushed to the managed registry,\n" +
			"server-side verification, and the journaled rollout, rendered live.\n" +
			"The environment's stored values apply unless --env-file is passed or\n" +
			"a discovered .env/.env.* file is selected interactively.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			if opts.BuildMode != "" && opts.BuildMode != "local" && opts.BuildMode != "auto" {
				return errors.New("--build must be local or auto (cloud builders arrive with R4)")
			}
			_, err := runDeployFlow(command, opts, false)
			return err
		},
	}
	addDeployFlags(command, opts)
	command.Flags().StringVar(&opts.BuildMode, "build", "auto",
		"build executor: local or auto (auto follows installation policy; local in R3)")
	command.Flags().BoolVar(&opts.Yes, "yes", false, "approve a non-destructive plan without prompting")
	command.Flags().BoolVar(&opts.AllowDestructive, "allow-destructive", false,
		"approve a destructive plan (non-interactive)")
	command.Flags().BoolVar(&opts.Detach, "detach", false, "do not attach to the run after completion")
	return command
}

func addDeployFlags(command *cobra.Command, opts *deployOptions) {
	command.Flags().StringVar(&opts.Environment, "environment", "",
		"target environment name (defaults to the checkout binding, prompted interactively otherwise)")
	command.Flags().StringVar(&opts.Manifest, "manifest", "", "explicit manifest path (skali.yml discovered by default)")
	command.Flags().StringVar(&opts.EnvFile, "env-file", "",
		"dotenv file to stage as candidate values (default: the environment's stored values)")
}
