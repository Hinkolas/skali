package main

import (
	"errors"

	"github.com/spf13/cobra"
)

func newPlanCommand() *cobra.Command {
	opts := &deployOptions{UseBinding: true}
	command := &cobra.Command{
		Use:   "plan",
		Short: "Preview the deployment plan without building or rolling out workloads",
		Long: "Compiles and submits the candidate definition, then prints the server's\n" +
			"plan without building images, uploading env-file values, or changing the\n" +
			"environment's active revision. Local review and env-file selection may be\n" +
			"recorded. --env-file is validated locally; the plan uses stored values.\n" +
			"With --from, previews promotion of the source's active revision instead.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			if err := validateFromFlags(command, opts); err != nil {
				return err
			}
			if opts.From != "" {
				_, err := runPromoteFlow(command, opts, true)
				return err
			}
			_, err := runDeployFlow(command, opts, true)
			return err
		},
	}
	addDeployFlags(command, opts)
	command.Flags().BoolVar(&opts.Rebuild, "rebuild", false,
		"plan as if artifacts were rebuilt and re-imported without reuse")
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
			"Values come from --env-file, else the env file (or stored values)\n" +
			"remembered for the environment in .skali/, else a one-time pick among\n" +
			"the checkout's .env/.env.* files that is remembered from then on;\n" +
			"--pick-env-file asks again. Without env files the stored values apply.\n\n" +
			"Builds and pushes need this terminal, so Ctrl-C during them (or\n" +
			"closing the terminal) cancels the deployment. Once they are done the\n" +
			"run continues on the server and the live view says so: d detaches\n" +
			"and leaves it running, Ctrl-C pressed twice cancels it. --detach\n" +
			"returns as soon as the run is handed over.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			if opts.BuildMode != "" && opts.BuildMode != "local" && opts.BuildMode != "auto" {
				return errors.New("--build must be local or auto (cloud builders are not available yet)")
			}
			if err := validateFromFlags(command, opts); err != nil {
				return err
			}
			if opts.Rebuild {
				opts.Force = true
			}
			if opts.From != "" {
				_, err := runPromoteFlow(command, opts, false)
				return err
			}
			_, err := runDeployFlow(command, opts, false)
			return err
		},
	}
	addDeployFlags(command, opts)
	command.Flags().StringVar(&opts.BuildMode, "build", "auto",
		"build executor: local or auto (auto follows installation policy; local today)")
	command.Flags().BoolVar(&opts.Yes, "yes", false, "approve a non-destructive plan without prompting")
	command.Flags().BoolVar(&opts.AllowDestructive, "allow-destructive", false,
		"approve a destructive plan (non-interactive)")
	command.Flags().BoolVar(&opts.Detach, "detach", false, "return after builds and uploads hand the run to the server, without waiting for the rollout")
	command.Flags().BoolVar(&opts.Force, "force", false,
		"deploy even when nothing changed; application workloads are restarted (data is untouched)")
	command.Flags().BoolVar(&opts.Rebuild, "rebuild", false,
		"rebuild and re-import artifacts without caches, picking up moved base images (implies --force)")
	return command
}

func addDeployFlags(command *cobra.Command, opts *deployOptions) {
	command.Flags().StringVar(&opts.Remote, "remote", "",
		"remote to target for this one invocation, ignoring the checkout binding and the current remote")
	command.Flags().StringVar(&opts.Environment, "environment", "",
		"target environment name (defaults to the checkout binding, prompted interactively otherwise)")
	command.Flags().StringVar(&opts.Manifest, "manifest", "", "explicit manifest path (otherwise discover skali.yaml or skali.yml up from the working directory)")
	command.Flags().StringVar(&opts.EnvFile, "env-file", "",
		"dotenv file to stage as candidate values for this run only (default: remembered choice; with --from: stored target values; plan only validates the file)")
	command.Flags().BoolVar(&opts.PickEnvFile, "pick-env-file", false,
		"ask which env file (or the stored values) to use and remember the answer for the environment (not used with --from)")
	command.Flags().StringVar(&opts.Platform, "platform", "",
		"override the build platform(s), e.g. linux/amd64 or a comma list (default: application platforms intersected with cluster platforms and preference)")
	command.Flags().StringVar(&opts.From, "from", "",
		"promote the active revision of another environment of this project; nothing builds and no manifest is read")
	command.Flags().BoolVar(&opts.PruneValues, "prune-values", false,
		"remove stored values the manifest no longer references as part of this deployment")
	command.Flags().BoolVar(&opts.BypassProtection, "bypass-protection", false,
		"deploy into a promote-only environment anyway (environment admins with a recent login; recorded on the run)")
}

// validateFromFlags rejects build machinery combined with a promotion:
// --from re-deploys what the source environment already runs.
func validateFromFlags(command *cobra.Command, opts *deployOptions) error {
	if opts.From == "" {
		return nil
	}
	if opts.Rebuild || opts.Platform != "" || opts.Manifest != "" || command.Flags().Changed("build") {
		return errors.New("--from cannot be combined with --build, --platform, --rebuild, or --manifest")
	}
	return nil
}
