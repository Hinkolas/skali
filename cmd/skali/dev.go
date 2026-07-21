package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/localdev"
)

const localContextName = "local"
const localEnvironmentName = "local"

func newDevCommand() *cobra.Command {
	var envFile, skalidImage string
	var detach bool
	command := &cobra.Command{
		Use:   "dev",
		Short: "Run the project on the local skali platform",
		Long: "Bare skali dev is the complete paved path: it ensures the disposable\n" +
			"local platform (k3d cluster with in-cluster skalid, Postgres, and\n" +
			"registry), builds and deploys the current project, attaches to the\n" +
			"rollout, and follows the runtime logs; Ctrl-C detaches and leaves\n" +
			"the project running. Use -d to skip the log follow, skali dev down\n" +
			"to remove the project again, and skali dev ls to see everything on\n" +
			"the local platform. Local values never leave this machine.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			if _, err := ensureLocalPlatform(command, skalidImage, false); err != nil {
				return err
			}
			opts := &deployOptions{
				Environment:   localEnvironmentName,
				EnvFile:       envFile,
				AutoEnvFile:   true,
				Yes:           true,
				CreateMissing: true,
			}
			outcome, err := runDeployFlow(command, opts, false)
			if err != nil {
				return err
			}
			if err := printDevReady(command); err != nil {
				return err
			}
			// A user who already detached from the rollout with Ctrl-C is
			// not asking for more output.
			if detach || outcome == deployOutcomeDetached {
				return nil
			}
			api, environmentID, err := localProjectEnvironment(command)
			if err != nil {
				return err
			}
			fmt.Fprintln(command.OutOrStdout(),
				"\nfollowing logs; Ctrl-C detaches and leaves the project running")
			return followRuntimeLogs(command, api, environmentID, "")
		},
	}
	command.PersistentFlags().StringVar(&skalidImage, "skalid-image", "",
		"control-plane image for the local platform (defaults to the recorded or task dev:image build)")
	command.Flags().StringVar(&envFile, "env-file", "", "explicit local env file (defaults to ./.env when present)")
	command.Flags().BoolVarP(&detach, "detach", "d", false,
		"exit once the rollout settles instead of following runtime logs")

	up := &cobra.Command{
		Use:   "up",
		Short: "Fully converge the local platform (create, repair, upgrade)",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			_, err := ensureLocalPlatform(command, skalidImage, true)
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

	var purge, yes bool
	down := &cobra.Command{
		Use:   "down",
		Short: "Remove the project from the local platform; data is retained",
		Long: "Removes the current project's running workloads from the local\n" +
			"platform, like docker compose down: the cluster and every other\n" +
			"project keep running, and this project's volumes, values, and\n" +
			"revision history are retained, so the next skali dev brings it\n" +
			"back with its data. With --purge the project's local environment\n" +
			"is destroyed completely, including volumes and all values,\n" +
			"secrets, revisions, and history; that decision is one-way.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			return runDevDown(command, purge, yes)
		},
	}
	down.Flags().BoolVar(&purge, "purge", false, "destroy the environment completely, including volumes and all data")
	down.Flags().BoolVar(&yes, "yes", false, "skip the confirmation for --purge")

	ls := &cobra.Command{
		Use:   "ls",
		Short: "List projects on the local platform",
		Args:  cobra.NoArgs,
		RunE:  runDevLs,
	}

	stop := &cobra.Command{
		Use:   "stop",
		Short: "Stop the local platform; state is retained",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			if err := localdev.Stop(command.Context()); err != nil {
				return err
			}
			out := command.OutOrStdout()
			fmt.Fprintf(out, "%sstopped local platform; state is retained\n",
				clirender.StyleFor(out).Check())
			return nil
		},
	}

	var resetYes bool
	reset := &cobra.Command{
		Use:   "reset",
		Short: "Destroy the complete local installation",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			return runDevReset(command, resetYes)
		},
	}
	reset.Flags().BoolVar(&resetYes, "yes", false, "skip the confirmation")

	command.AddCommand(up, status, logs, down, ls, stop, reset)
	return command
}

// runDevDown tears the current project down on the local platform. Plain
// down is reversible (data is retained) and needs no confirmation; purge
// demands the typed project name unless --yes.
func runDevDown(command *cobra.Command, purge, yes bool) error {
	ctx := command.Context()
	out := command.OutOrStdout()
	style := clirender.StyleFor(out)
	project, err := loadLocalProject("")
	if err != nil {
		return err
	}
	name := project.Result.Definition.Name
	api, environmentID, err := localProjectEnvironment(command)
	if err != nil {
		return fmt.Errorf("%s is not on the local platform: %w", name, err)
	}

	if purge && !yes {
		fmt.Fprintln(out, style.BoldRed(fmt.Sprintf("This destroys the local environment of %s completely:", name)))
		fmt.Fprintln(out, "  its namespace including all volumes, and its values, secrets,")
		fmt.Fprintln(out, "  revisions, and history on the local platform.")
		fmt.Fprintln(out, "Nothing outside this machine is affected.")
		if !confirm(out, fmt.Sprintf("\nPurge %s from the local platform? [y/N] ", name)) {
			return errors.New("aborted")
		}
	}

	runID, err := api.TeardownEnvironment(ctx, environmentID, purge)
	if isReauthRequired(err) {
		if err := reauthLocal(ctx, api); err != nil {
			return err
		}
		runID, err = api.TeardownEnvironment(ctx, environmentID, purge)
	}
	if err != nil {
		return err
	}

	verb := "take down"
	if purge {
		verb = "purge"
	}
	fmt.Fprintf(out, "%s %s  %s %s\n", style.Dim("run"), style.Bold(runID), verb, name)
	status, err := attachRun(ctx, out, api, runID)
	if err != nil {
		// The purge epilogue deletes the environment row and every run
		// with it; losing the run mid-poll means the purge finished.
		if !purge || !isNotFound(err) {
			return err
		}
		status = "succeeded"
	}
	switch status {
	case "succeeded":
	case "detached":
		return nil
	default:
		return fmt.Errorf("run %s %s", runID, status)
	}

	if purge {
		if err := waitEnvironmentGone(ctx, api, environmentID); err != nil {
			return err
		}
		fmt.Fprintf(out, "\n%s%s is purged from the local platform; nothing of it remains\n",
			style.Check(), name)
		return nil
	}
	fmt.Fprintf(out, "\n%s%s is down; its data is retained\n", style.Check(), name)
	fmt.Fprintf(out, "  %s  skali dev\n", style.Dim("bring it back"))
	return nil
}

// waitEnvironmentGone polls until the purged environment's row is deleted;
// the 404 is the authoritative completion signal.
func waitEnvironmentGone(ctx context.Context, api *client.Client, environmentID string) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, err := api.GetEnvironment(ctx, environmentID)
		if isNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return errors.New("the purge is still finishing on the server; check skali dev ls")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// reauthLocal refreshes the sudo window with the recorded bootstrap
// credentials: the local platform's admin password already lives on this
// machine, so prompting would be theater.
func reauthLocal(ctx context.Context, api *client.Client) error {
	state, err := localdev.LoadState()
	if err != nil || state.AdminPassword == "" {
		return errors.New("recent authentication required; run skali login")
	}
	if err := api.Reauthenticate(ctx, state.AdminPassword); err != nil {
		return fmt.Errorf("reauthenticate against the local platform: %w", err)
	}
	return nil
}

func isReauthRequired(err error) bool {
	var apiErr *client.APIError
	return errors.As(err, &apiErr) && apiErr.Code == "reauth_required"
}

func isNotFound(err error) bool {
	var apiErr *client.APIError
	return errors.As(err, &apiErr) && apiErr.Status == 404
}

// runDevLs lists every project on the local platform with the state of its
// environments, the docker compose ls of the local cluster.
func runDevLs(command *cobra.Command, args []string) error {
	ctx := command.Context()
	out := command.OutOrStdout()
	cfg, err := cliconfig.Load()
	if err != nil {
		return err
	}
	localContext := cfg.Contexts[localContextName]
	if localContext == nil {
		return errors.New("the local platform is not set up; run skali dev up first")
	}
	api := client.New(localContext.Master, localContext.Token, userAgent())
	projects, err := api.ListProjects(ctx)
	if err != nil {
		return err
	}
	style := clirender.StyleFor(out)
	fmt.Fprintln(out, style.Dim(fmt.Sprintf("%-24s  %-13s  %-10s  %s", "PROJECT", "ENVIRONMENT", "STATE", "ACTIVE")))
	for _, project := range projects {
		environments, err := api.ListEnvironments(ctx, project.ID)
		if err != nil {
			return err
		}
		for _, environment := range environments {
			state, active := "unknown", "-"
			if status, err := api.EnvironmentStatus(ctx, environment.ID); err == nil {
				state = status.State
				if status.ActiveRevision != nil {
					active = shortChecksum(status.ActiveRevision.Checksum)
				}
			}
			fmt.Fprintf(out, "%-24s  %-13s  %s  %s\n", project.Name, environment.Name,
				stateColor(style, fmt.Sprintf("%-10s", state)), active)
		}
	}
	return nil
}

// taskProgress renders localdev ensure stages through the live task
// printer; an unconcluded stage on error settles as failed via Abort.
type taskProgress struct {
	tasks   *clirender.Tasks
	current *clirender.Task
}

func (p *taskProgress) Start(title string) {
	if p.current != nil {
		p.current.Done("")
	}
	p.current = p.tasks.Start(title)
}

func (p *taskProgress) Done(detail string) {
	if p.current == nil {
		return
	}
	p.current.Done(detail)
	p.current = nil
}

func (p *taskProgress) Skip(detail string) {
	if p.current == nil {
		return
	}
	p.current.Skip(detail)
	p.current = nil
}

func (p *taskProgress) Abort() {
	if p.current == nil {
		return
	}
	p.current.Fail()
	p.current = nil
}

// ensureLocalPlatform brings the platform up and logs the CLI into it,
// storing the local context.
func ensureLocalPlatform(command *cobra.Command, skalidImage string, forceConverge bool) (*localdev.State, error) {
	ctx := command.Context()
	out := command.OutOrStdout()

	if status, err := localdev.Status(ctx); err == nil && status != localdev.ClusterRunning {
		fmt.Fprintln(out, "Local platform is not running. Creating it now.")
	}
	tasks := clirender.NewTasks(out)
	if skalidImage == "" {
		skalidImage = defaultSkalidImage(ctx, tasks)
	}
	progress := &taskProgress{tasks: tasks}
	state, err := localdev.Ensure(ctx, localdev.EnsureOptions{
		SkalidImage:   skalidImage,
		ForceConverge: forceConverge,
		Progress:      progress,
	})
	if err != nil {
		progress.Abort()
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
func defaultSkalidImage(ctx context.Context, tasks *clirender.Tasks) string {
	if state, err := localdev.LoadState(); err == nil && state.SkalidImage != "" {
		return state.SkalidImage
	}
	if root := findRepoRoot(); root != "" {
		task := tasks.Start("Build skalid:dev from the working tree")
		if err := localdev.BuildSkalidImage(ctx, root, "skalid:dev", task.NoteWriter()); err == nil {
			task.Done("")
			return "skalid:dev"
		}
		task.Fail()
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
	style := clirender.StyleFor(out)
	clusterStatus, err := localdev.Status(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "platform   %s (cluster %s, %s)\n",
		stateColor(style, string(clusterStatus)), localdev.ClusterName(), localdev.K3sImage)
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
	switch status.State {
	case "down":
		fmt.Fprintf(out, "project    %s (workloads removed; data retained; skali dev brings it back)\n",
			stateColor(style, "down"))
		return nil
	case "releasing":
		fmt.Fprintf(out, "project    %s (purge in progress)\n", stateColor(style, "releasing"))
		return nil
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
		fmt.Fprintf(out, "  %-24s %s %s%d/%d ready\n",
			service.Type+"."+service.Key,
			stateColor(style, fmt.Sprintf("%-11s", service.Health)),
			replicaDots(style, ready, len(service.Pods)), ready, len(service.Pods))
		for _, diagnostic := range service.Diagnostics {
			fmt.Fprintf(out, "    %s %s\n",
				severityColor(style, diagnostic.Severity+":"), diagnostic.Message)
		}
	}
	return nil
}

// stateColor paints a lifecycle word by its meaning; padding around the
// word survives because the switch trims before matching.
func stateColor(style *clirender.Style, state string) string {
	switch strings.TrimSpace(state) {
	case "running", "healthy", "active", "succeeded", "ready":
		return style.Green(state)
	case "stopped", "down", "releasing", "progressing", "degraded", "waiting", "pending":
		return style.Yellow(state)
	case "failed", "unhealthy", "error", "cancelled":
		return style.Red(state)
	}
	return state
}

func severityColor(style *clirender.Style, severity string) string {
	switch strings.TrimSuffix(severity, ":") {
	case "error", "fatal":
		return style.Red(severity)
	case "warning":
		return style.Yellow(severity)
	}
	return style.Dim(severity)
}

// replicaDots draws one dot per replica, green when ready; plain output
// keeps the bare counts.
func replicaDots(style *clirender.Style, ready, total int) string {
	if !style.Enabled || total == 0 {
		return ""
	}
	dots := style.Green(strings.Repeat("●", ready))
	if total > ready {
		dots += style.Dim(strings.Repeat("○", total-ready))
	}
	return dots + " "
}

func runDevReset(command *cobra.Command, yes bool) error {
	ctx := command.Context()
	out := command.OutOrStdout()
	style := clirender.StyleFor(out)
	if !yes {
		fmt.Fprintln(out, style.BoldRed("This destroys the complete local installation:"))
		fmt.Fprintf(out, "  cluster %s, its volumes, the local registry and its artifacts,\n", localdev.ClusterName())
		fmt.Fprintln(out, "  local Skali state, and all locally deployed project data.")
		fmt.Fprintln(out, "Nothing outside this machine is affected.")
		if !confirm(out, "\nDestroy the local installation? [y/N] ") {
			return errors.New("aborted")
		}
	}

	tasks := clirender.NewTasks(out)
	if status, err := localdev.Status(ctx); err == nil && status != localdev.ClusterAbsent {
		task := tasks.Start("Delete cluster " + localdev.ClusterName() + " and volumes")
		if err := localdev.Delete(ctx); err != nil {
			task.Fail()
			return err
		}
		task.Done("")
	}
	task := tasks.Start("Remove local installation record")
	if err := localdev.RemoveState(); err != nil {
		task.Fail()
		return err
	}
	task.Done("")

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
	style := clirender.StyleFor(out)
	fmt.Fprintf(out, "  %s  %s\n", style.Dim("dashboard"), style.Cyan(localdev.MasterURL()))
	fmt.Fprintf(out, "  %s     http://<domain>:%d for your manifest's *.localhost domains\n",
		style.Dim("routes"), localdev.HTTPPort())
	return nil
}
