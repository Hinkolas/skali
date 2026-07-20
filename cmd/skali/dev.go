package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/localdev"
)

const localContextName = "local"
const localEnvironmentName = "local"

func newDevCommand() *cobra.Command {
	var envFile, skalidImage string
	command := &cobra.Command{
		Use:   "dev",
		Short: "Run the project on the local skali platform",
		Long: "Bare skali dev is the complete paved path: it ensures the disposable\n" +
			"local platform (k3d cluster with in-cluster skalid, Postgres, and\n" +
			"registry), builds and deploys the current project, and attaches to\n" +
			"the rollout. Local values never leave this machine.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			if _, err := ensureLocalPlatform(command, skalidImage); err != nil {
				return err
			}
			opts := &deployOptions{
				Environment:   localEnvironmentName,
				EnvFile:       envFile,
				AutoEnvFile:   true,
				Yes:           true,
				CreateMissing: true,
			}
			if err := runDeployFlow(command, opts, false); err != nil {
				return err
			}
			return printDevReady(command)
		},
	}
	command.PersistentFlags().StringVar(&skalidImage, "skalid-image", "",
		"control-plane image for the local platform (defaults to the recorded or task dev:image build)")
	command.Flags().StringVar(&envFile, "env-file", "", "explicit local env file (defaults to ./.env when present)")

	up := &cobra.Command{
		Use:   "up",
		Short: "Ensure the local platform only",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			_, err := ensureLocalPlatform(command, skalidImage)
			return err
		},
	}

	status := &cobra.Command{
		Use:   "status",
		Short: "Show platform and current-project state",
		Args:  cobra.NoArgs,
		RunE:  runDevStatus,
	}

	logs := &cobra.Command{
		Use:   "logs [service]",
		Short: "Stream the local project's runtime logs",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			service := ""
			if len(args) == 1 {
				service = args[0]
			}
			api, environmentID, err := localProjectEnvironment(command)
			if err != nil {
				return err
			}
			return streamRuntimeLogs(command, api, environmentID, service)
		},
	}

	stop := &cobra.Command{
		Use:   "stop",
		Short: "Stop the local platform; state is retained",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			if err := localdev.Stop(command.Context()); err != nil {
				return err
			}
			fmt.Fprintln(command.OutOrStdout(), "stopped local platform; state is retained")
			return nil
		},
	}

	reset := &cobra.Command{
		Use:   "reset",
		Short: "Destroy the complete local installation",
		Args:  cobra.NoArgs,
		RunE:  runDevReset,
	}

	command.AddCommand(up, status, logs, stop, reset)
	return command
}

// ensureLocalPlatform brings the platform up and logs the CLI into it,
// storing the local context.
func ensureLocalPlatform(command *cobra.Command, skalidImage string) (*localdev.State, error) {
	ctx := command.Context()
	out := command.OutOrStdout()

	if status, err := localdev.Status(ctx); err == nil && status != localdev.ClusterRunning {
		fmt.Fprintln(out, "Local platform is not running. Creating it now.")
	}
	if skalidImage == "" {
		skalidImage = defaultSkalidImage(ctx, out)
	}
	state, err := localdev.Ensure(ctx, localdev.EnsureOptions{
		SkalidImage: skalidImage,
		Log: func(format string, args ...any) {
			fmt.Fprintf(out, format+"\n", args...)
		},
	})
	if err != nil {
		return nil, err
	}
	if err := loginLocalContext(ctx, state); err != nil {
		return nil, err
	}
	return state, nil
}

// defaultSkalidImage prefers the recorded image, then a working-tree build
// when the CLI runs inside the repository (the developer path until
// published bootstrap images exist).
func defaultSkalidImage(ctx context.Context, out io.Writer) string {
	if state, err := localdev.LoadState(); err == nil && state.SkalidImage != "" {
		return state.SkalidImage
	}
	if root := findRepoRoot(); root != "" {
		fmt.Fprintln(out, "building skalid:dev from the working tree")
		if err := localdev.BuildSkalidImage(ctx, root, "skalid:dev"); err == nil {
			return "skalid:dev"
		}
	}
	return ""
}

// findRepoRoot detects a skali working tree by its skalid Dockerfile.
func findRepoRoot() string {
	directory, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "build", "skalid.Dockerfile")); err == nil {
			if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
				return directory
			}
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return ""
		}
		directory = parent
	}
}

// loginLocalContext authenticates against the local installation with the
// recorded bootstrap credentials and stores the context.
func loginLocalContext(ctx context.Context, state *localdev.State) error {
	cfg, err := cliconfig.Load()
	if err != nil {
		return err
	}
	existing := cfg.Contexts[localContextName]
	if existing != nil && existing.Token != "" {
		probe := client.New(localdev.MasterURL(), existing.Token, userAgent())
		if _, err := probe.CurrentSession(ctx); err == nil {
			cfg.CurrentContext = localContextName
			return cliconfig.Save(cfg)
		}
	}
	api := client.New(localdev.MasterURL(), "", userAgent())
	result, err := api.Login(ctx, state.AdminEmail, state.AdminPassword)
	if err != nil {
		return fmt.Errorf("log in to the local platform: %w", err)
	}
	if result.Session == nil {
		return errors.New("the local platform unexpectedly demanded a second factor")
	}
	if cfg.Contexts == nil {
		cfg.Contexts = map[string]*cliconfig.Context{}
	}
	cfg.Contexts[localContextName] = &cliconfig.Context{
		Master: localdev.MasterURL(),
		Token:  result.Session.Token,
	}
	cfg.CurrentContext = localContextName
	return cliconfig.Save(cfg)
}

// localProjectEnvironment resolves the current project's local environment
// through the local context.
func localProjectEnvironment(command *cobra.Command) (*client.Client, string, error) {
	project, err := loadLocalProject("")
	if err != nil {
		return nil, "", err
	}
	cfg, err := cliconfig.Load()
	if err != nil {
		return nil, "", err
	}
	localContext := cfg.Contexts[localContextName]
	if localContext == nil {
		return nil, "", errors.New("the local platform is not set up; run skali dev up first")
	}
	api := client.New(localContext.Master, localContext.Token, userAgent())
	_, environmentID, err := resolveEnvironmentIDs(command.Context(), api,
		project.Result.Definition.Name, localEnvironmentName, false)
	if err != nil {
		return nil, "", err
	}
	return api, environmentID, nil
}

func runDevStatus(command *cobra.Command, args []string) error {
	ctx := command.Context()
	out := command.OutOrStdout()
	clusterStatus, err := localdev.Status(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "platform   %s (cluster %s, %s)\n", clusterStatus, localdev.ClusterName(), localdev.K3sImage)
	if clusterStatus != localdev.ClusterRunning {
		return nil
	}
	api, environmentID, err := localProjectEnvironment(command)
	if err != nil {
		fmt.Fprintf(out, "project    (not deployed: %v)\n", err)
		return nil
	}
	status, err := api.EnvironmentStatus(ctx, environmentID)
	if err != nil {
		return err
	}
	active := "none"
	if status.ActiveRevision != nil {
		active = shortChecksum(status.ActiveRevision.Checksum)
	}
	fmt.Fprintf(out, "project    active revision %s (observation %s)\n", active, status.Observation.State)
	for _, service := range status.Services {
		ready := 0
		for _, pod := range service.Pods {
			if pod.Ready {
				ready++
			}
		}
		fmt.Fprintf(out, "  %-24s %-11s %d/%d ready\n",
			service.Type+"."+service.Key, service.Health, ready, len(service.Pods))
		for _, diagnostic := range service.Diagnostics {
			fmt.Fprintf(out, "    %s: %s\n", diagnostic.Severity, diagnostic.Message)
		}
	}
	return nil
}

func runDevReset(command *cobra.Command, args []string) error {
	ctx := command.Context()
	out := command.OutOrStdout()
	fmt.Fprintln(out, "This destroys the complete local installation:")
	fmt.Fprintf(out, "  cluster %s, its volumes, the local registry and its artifacts,\n", localdev.ClusterName())
	fmt.Fprintln(out, "  local Skali state, and all locally deployed project data.")
	fmt.Fprintln(out, "Nothing outside this machine is affected.")
	fmt.Fprint(out, "\nType \"destroy\" to continue: ")
	var answer string
	_, _ = fmt.Scanln(&answer)
	if strings.TrimSpace(answer) != "destroy" {
		return errors.New("aborted")
	}
	if err := localdev.Reset(ctx); err != nil {
		return err
	}
	fmt.Fprintf(out, "  ok  Delete cluster %s and volumes\n", localdev.ClusterName())
	fmt.Fprintln(out, "  ok  Remove local installation record")

	// Drop the stored local context; its token died with the cluster.
	if cfg, err := cliconfig.Load(); err == nil {
		delete(cfg.Contexts, localContextName)
		if cfg.CurrentContext == localContextName {
			cfg.CurrentContext = ""
		}
		_ = cliconfig.Save(cfg)
	}
	return nil
}

func printDevReady(command *cobra.Command) error {
	out := command.OutOrStdout()
	fmt.Fprintf(out, "  dashboard  %s\n", localdev.MasterURL())
	fmt.Fprintf(out, "  routes     http://<domain>:%d for your manifest's *.localhost domains\n", localdev.HTTPPort())
	return nil
}
