package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/devports"
	"github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/localdev"
	"github.com/Hinkolas/skali/internal/manifest"
	"github.com/Hinkolas/skali/internal/utils"
	versionpkg "github.com/Hinkolas/skali/internal/version"
)

const localRemoteName = cliconfig.LocalRemoteName
const localEnvironmentName = "local"

func newDevCommand() *cobra.Command {
	var envFile, skalidImage, platform string
	var detach, force, rebuild, preview, pruneValues bool
	command := &cobra.Command{
		Use:   "dev",
		Short: "Run the project on the local skali platform",
		Long: "Bare skali dev is the complete paved path: it ensures the disposable\n" +
			"local platform (k3d cluster with in-cluster skalid, Postgres, and\n" +
			"registry), builds and deploys the current project, attaches to the\n" +
			"rollout, and follows the runtime logs. Like docker compose, ending\n" +
			"the session (Ctrl-C, closing the terminal) pauses the project; its\n" +
			"data is retained and the next skali dev brings it back. Pressing d\n" +
			"while the logs follow detaches instead: the session ends and the\n" +
			"project keeps running, as if started with -d. A rollout\n" +
			"already in flight is adopted: dev attaches to it instead of\n" +
			"failing; --force cancels it and redeploys. Use -d for a background\n" +
			"project that keeps running, skali dev down to pause it explicitly,\n" +
			"and skali dev ls to see everything on the local platform. Local\n" +
			"values never leave this machine.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			// The whole session runs on one signal-scoped context: INT,
			// TERM, and HUP (a closed terminal) all end it, and the
			// epilogue pauses the project unless -d asked for a background
			// project.
			sessionCtx, stopSignals := signal.NotifyContext(command.Context(),
				os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
			defer stopSignals()
			command.SetContext(sessionCtx)

			var window atomic.Value // open artifact window's deployment ID
			window.Store("")

			// Dev-block applications run on the host instead of building,
			// unless --preview asks for the full in-cluster deployment.
			// They are child processes of this session, so a detached
			// session cannot host them.
			project, err := loadLocalProject("")
			if err != nil {
				return err
			}
			devApps := map[string]manifest.Dev{}
			if !preview {
				devApps = devApplications(project)
			}
			if detach && len(devApps) > 0 {
				return fmt.Errorf("--detach cannot host local dev processes (%s); "+
					"use --preview for a detached full deployment",
					strings.Join(utils.SortedKeys(devApps), ", "))
			}
			// Host ports are resolved up front so a busy pin fails before
			// any platform or server work: manifest pins verbatim, every
			// other service port deterministically auto-allocated. One
			// allocator spans the session so applications never collide.
			devPorts := map[string]map[string]int{}
			if len(devApps) > 0 {
				allocator := devports.Default()
				for _, key := range utils.SortedKeys(devApps) {
					allocated, err := allocator.Allocate(
						project.Result.Definition.Name, key,
						kubernetes.InterceptPortNames(project.Result.Definition.Applications[key]),
						devApps[key].Ports)
					if err != nil {
						return err
					}
					devPorts[key] = allocated
				}
			}

			if _, err := ensureLocalPlatform(command, skalidImage, false); err != nil {
				if sessionCtx.Err() != nil {
					return errors.New("interrupted")
				}
				return err
			}
			// followWithChildren starts the host dev processes (none under
			// --preview) and hands the session to the log follow.
			followWithChildren := func(api *client.Client, environmentID string) error {
				var children *devChildren
				var mux *logMux
				if len(devApps) > 0 {
					mux = newLogMux(command.OutOrStdout())
					var err error
					children, err = startDevChildren(sessionCtx, mux, command.OutOrStdout(),
						api, environmentID, project.Root, devApps, devPorts)
					if err != nil {
						if sessionCtx.Err() != nil {
							return finishInterrupted(command, window.Load().(string), detach)
						}
						return err
					}
				}
				return devFollowLogs(command, api, environmentID, &window, children, mux)
			}
			// A run already holding the environment's slot (a rollout still
			// settling, a pause finishing) is resolved before the deploy
			// flow: adopt it instead of failing with deployment_in_flight,
			// or cancel it when --force asked for a fresh deploy. A failed
			// environment lookup means nothing is deployed yet.
			attachToRunning := func(api *client.Client, environmentID string) error {
				printDevReady(command, api, environmentID, devPorts)
				if detach {
					return nil
				}
				if len(devApps) > 0 {
					out := command.OutOrStdout()
					fmt.Fprintf(out, "  %s\n", clirender.StyleFor(out).Yellow(
						"note: attached to an in-flight run; route interception may lag until the next deploy"))
				}
				return followWithChildren(api, environmentID)
			}
			if api, environmentID, err := localProjectEnvironment(command); err == nil {
				action, err := devResolveInFlight(sessionCtx, command.OutOrStdout(),
					api, environmentID, force || rebuild)
				if sessionCtx.Err() != nil {
					return finishInterrupted(command, window.Load().(string), detach)
				}
				if err != nil {
					return err
				}
				switch action {
				case devInFlightDetached:
					return nil
				case devInFlightAttached:
					return attachToRunning(api, environmentID)
				}
			}
			opts := &deployOptions{
				Remote:             localRemoteName,
				Environment:        localEnvironmentName,
				EnvFile:            envFile,
				AutoEnvFile:        true,
				Yes:                true,
				CreateMissing:      true,
				Platform:           platform,
				Force:              force || rebuild,
				Rebuild:            rebuild,
				PruneValues:        pruneValues,
				OnDeploymentOpened: func(id string) { window.Store(id) },
				OnDeploymentClosed: func() { window.Store("") },
				SkipReadySummary:   true,
			}
			if len(devApps) > 0 {
				opts.LocalApplications = make(map[string]client.LocalApplication, len(devApps))
				for key := range devApps {
					opts.LocalApplications[key] = client.LocalApplication{Ports: devPorts[key]}
				}
			}
			var outcome string
			for attempt := 0; ; attempt++ {
				var err error
				outcome, err = runDeployFlow(command, opts, false)
				if sessionCtx.Err() != nil {
					return finishInterrupted(command, window.Load().(string), detach)
				}
				if err == nil {
					break
				}
				// A run can still take the slot between the up-front check
				// and the open (a cancelled deployment's fallback reconcile,
				// a concurrent session); resolve it the same way instead of
				// surfacing the 409, with a cap so a pathological server
				// cannot loop us forever.
				if !isDeploymentInFlight(err) || attempt >= 2 {
					return err
				}
				api, environmentID, lookupErr := localProjectEnvironment(command)
				if lookupErr != nil {
					return err
				}
				action, resolveErr := devResolveInFlight(sessionCtx, command.OutOrStdout(),
					api, environmentID, force || rebuild)
				if sessionCtx.Err() != nil {
					return finishInterrupted(command, window.Load().(string), detach)
				}
				if resolveErr != nil {
					return resolveErr
				}
				switch action {
				case devInFlightDetached:
					return nil
				case devInFlightAttached:
					return attachToRunning(api, environmentID)
				}
			}
			api, environmentID, err := localProjectEnvironment(command)
			if err != nil {
				return err
			}
			printDevReady(command, api, environmentID, devPorts)
			if detach || outcome == deployOutcomeDetached {
				return nil
			}
			return followWithChildren(api, environmentID)
		},
	}
	command.PersistentFlags().StringVar(&skalidImage, "skalid-image", "",
		"control-plane image for the local platform (defaults to the recorded or task dev:image build)")
	command.Flags().StringVar(&envFile, "env-file", "", "explicit local env file (defaults to ./.env; otherwise discovered env files are offered)")
	command.Flags().StringVar(&platform, "platform", "",
		"override the build platform(s), e.g. linux/amd64 or a comma list (default: the cluster architecture)")
	command.Flags().BoolVarP(&detach, "detach", "d", false,
		"exit once the rollout settles instead of following runtime logs")
	command.Flags().BoolVar(&force, "force", false,
		"deploy even when nothing changed, cancelling any in-flight run first; application workloads are restarted (data is untouched)")
	command.Flags().BoolVar(&rebuild, "rebuild", false,
		"rebuild and re-import artifacts without caches, picking up moved base images (implies --force)")
	command.Flags().BoolVar(&preview, "preview", false,
		"build and deploy every application in the cluster, ignoring dev blocks; no local dev processes run")
	command.Flags().BoolVar(&pruneValues, "prune-values", false,
		"remove stored values the manifest no longer references as part of this deployment")

	up := &cobra.Command{
		Use:   "up",
		Short: "Fully converge the local platform (create, repair)",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			_, err := ensureLocalPlatform(command, skalidImage, true)
			return err
		},
	}

	upgrade := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade the local platform to this CLI's skalid version",
		Long: "Moves the local platform's control plane to the skalid this CLI\n" +
			"ships: the working-tree build inside the skali repository, the\n" +
			"published image of the same version for a released CLI. Bare\n" +
			"skali dev and skali dev up repair the platform but never change\n" +
			"its version; this command is the one that does. Project data is\n" +
			"retained. Downgrades are refused.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			return runDevUpgrade(command, skalidImage)
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
			"revisions, and history; that decision is one-way.",
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

	start := &cobra.Command{
		Use:   "start",
		Short: "Start the stopped local platform; state is retained",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			clusterStatus, err := localdev.Status(command.Context())
			if err != nil {
				return err
			}
			if clusterStatus == localdev.ClusterAbsent {
				return errors.New("the local platform is not installed; run skali dev up first")
			}
			if _, err := ensureLocalPlatform(command, skalidImage, false); err != nil {
				return err
			}
			out := command.OutOrStdout()
			fmt.Fprintf(out, "%slocal platform running; state is retained\n",
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

	command.AddCommand(up, upgrade, status, logs, newDevExecCommand(), newDevRunCommand(),
		newDevValuesCommand(), down, ls, stop, start, reset)
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
		fmt.Fprintln(out, "  its namespace including all volumes, and its values,")
		fmt.Fprintln(out, "  revisions, and history on the local platform.")
		fmt.Fprintln(out, "Nothing outside this machine is affected.")
		confirmed, err := cliprompt.New(os.Stdin, out).Confirm(ctx, cliprompt.ConfirmOptions{
			Title: fmt.Sprintf("Purge %s from the local platform?", name),
		})
		if err != nil {
			return err
		}
		if !confirmed {
			return errors.New("aborted")
		}
	}

	status, err := teardownLocalEnvironment(ctx, out, api, environmentID, name, purge)
	if err != nil {
		return err
	}
	if status == "detached" || status == "interrupted" {
		return nil
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

// teardownLocalEnvironment issues the teardown (with the recorded-password
// reauth retry) and attaches to its run; shared by skali dev down and the
// session's pause-on-exit.
func teardownLocalEnvironment(ctx context.Context, out io.Writer, api *client.Client,
	environmentID, name string, purge bool) (string, error) {
	style := clirender.StyleFor(out)
	runID, err := api.TeardownEnvironment(ctx, environmentID, purge)
	if isReauthRequired(err) {
		if err := reauthLocal(ctx, api); err != nil {
			return "", err
		}
		runID, err = api.TeardownEnvironment(ctx, environmentID, purge)
	}
	if err != nil {
		return "", err
	}

	verb := "take down"
	if purge {
		verb = "purge"
	}
	fmt.Fprintf(out, "%s %s  %s %s\n", style.Dim("run"), style.Bold(runID), verb, name)
	status, err := attachRun(ctx, out, api, runID, localRemoteName)
	if err != nil {
		// The purge epilogue deletes the environment row and every run
		// with it; losing the run mid-poll means the purge finished.
		if !purge || !isNotFound(err) {
			return "", err
		}
		status = "succeeded"
	}
	switch status {
	case "succeeded", "detached", "interrupted":
		return status, nil
	default:
		return "", fmt.Errorf("run %s %s", runID, status)
	}
}

// finishInterrupted is the signaled session's epilogue, on a fresh context
// (the session context is already dead and on SIGHUP the terminal may be
// gone, so prints are best-effort): close an interrupted artifact window so
// the pause is not refused as an in-flight deployment, then pause the
// project unless -d asked for a background one.
func finishInterrupted(command *cobra.Command, window string, keepRunning bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	out := command.OutOrStdout()
	style := clirender.StyleFor(out)

	project, projectErr := loadLocalProject("")
	cfg, cfgErr := cliconfig.Load()
	if projectErr != nil || cfgErr != nil || cfg.Remotes[localRemoteName] == nil {
		return errors.New("interrupted")
	}
	localRemote := cfg.Remotes[localRemoteName]
	api := remoteClient(cfg, localRemote)

	if window != "" {
		// The interrupted build client owns the open window; failing it
		// discards the staged values and unblocks the pause immediately
		// instead of after the stale-build sweep.
		if err := api.FailDeployment(ctx, window); err != nil {
			fmt.Fprintf(out, "%s\n", style.Dim("close interrupted deployment: "+err.Error()))
		}
	}
	if keepRunning {
		return errors.New("interrupted")
	}
	name := project.Result.Definition.Name
	_, environmentID, err := resolveEnvironmentIDs(ctx, api, name, localEnvironmentName)
	if err != nil {
		// Nothing deployed yet, nothing to pause.
		return errors.New("interrupted")
	}
	fmt.Fprintf(out, "\npausing %s\n", name)
	status, err := teardownLocalEnvironment(ctx, out, api, environmentID, name, false)
	if err != nil {
		return err
	}
	switch status {
	case "detached":
		fmt.Fprintln(out, "the pause continues on the server; check skali dev ls")
		return nil
	case "interrupted":
		// The epilogue's own deadline expired while the server was still
		// finishing; claiming a completed pause here would be a guess.
		fmt.Fprintln(out, "the pause is still finishing on the server; check skali dev ls")
		return nil
	}
	fmt.Fprintf(out, "\n%s%s is paused; its data is retained\n", style.Check(), name)
	fmt.Fprintf(out, "  %s  skali dev\n", style.Dim("bring it back"))
	return nil
}

// dev in-flight resolutions: what devResolveInFlight decided about a run
// already holding the environment's slot.
const (
	devInFlightProceed  = "proceed"  // slot free (or freed): run the deploy flow
	devInFlightAttached = "attached" // adopted a deployment run to success: skip the deploy flow
	devInFlightDetached = "detached" // the user detached during the attach; the session epilogue decides
)

// devResolveInFlight inspects the local environment's run slot before the
// deploy flow. A running deployment run is adopted (attach instead of the
// deployment_in_flight error) and --force cancels it for a fresh deploy. A
// running teardown or reconcile is waited out but never cancelled without
// --force: their journal rows do not drive the underlying work, so
// cancelling would only misreport it.
func devResolveInFlight(ctx context.Context, out io.Writer, api *client.Client,
	environmentID string, force bool) (string, error) {
	running, err := findRunningRun(ctx, api, environmentID)
	if err != nil || running == nil {
		// Lookup errors resurface in the deploy flow with full context.
		return devInFlightProceed, nil
	}
	style := clirender.StyleFor(out)
	if force {
		fmt.Fprintf(out, "cancelling in-flight %s run %s (--force)\n",
			running.Kind, style.Bold(running.ID))
		if _, err := api.CancelRun(ctx, running.ID); err != nil && !isRunAlreadyFinished(err) {
			return "", err
		}
		return devInFlightProceed, nil
	}
	if running.Kind != "deployment" {
		fmt.Fprintf(out, "a %s is in flight; waiting for run %s to finish\n",
			running.Kind, style.Bold(running.ID))
		status, err := attachRun(ctx, out, api, running.ID, localRemoteName)
		if err != nil {
			return "", err
		}
		if status == "detached" || status == "interrupted" {
			return devInFlightDetached, nil
		}
		return devInFlightProceed, nil
	}
	fmt.Fprintf(out, "a deployment is already in flight; attaching to run %s\n",
		style.Bold(running.ID))
	status, err := attachRun(ctx, out, api, running.ID, localRemoteName)
	if err != nil {
		return "", err
	}
	switch status {
	case "succeeded":
		fmt.Fprintln(out, "\n"+style.Check()+style.Bold(style.Green("ready")))
		return devInFlightAttached, nil
	case "failed":
		return "", fmt.Errorf("run %s failed", running.ID)
	case "cancelled":
		return "", fmt.Errorf("run %s was cancelled", running.ID)
	default:
		return devInFlightDetached, nil
	}
}

// findRunningRun returns the environment's running run, or nil. At most one
// can exist (the journal's unique running-run index); pending runs never
// hold the slot.
func findRunningRun(ctx context.Context, api *client.Client, environmentID string) (*client.Run, error) {
	runs, err := api.ListRuns(ctx, environmentID)
	if err != nil {
		return nil, err
	}
	for index := range runs {
		if runs[index].Status == "running" {
			return &runs[index], nil
		}
	}
	return nil, nil
}

// devFollowLogs hands the rest of the session to the runtime logs. On a
// terminal the d key detaches: the follow ends and the project keeps
// running, exactly as if the session had started with -d; host dev
// processes are terminated first, since they cannot outlive the CLI.
// Everything else that ends the logs terminates the children and reaches
// the epilogue, which pauses the project.
func devFollowLogs(command *cobra.Command, api *client.Client, environmentID string,
	window *atomic.Value, children *devChildren, mux *logMux) error {
	out := command.OutOrStdout()
	sessionCtx := command.Context()
	followCtx := sessionCtx
	var detached atomic.Bool
	keys, restoreTerminal := watchDetachKey()
	defer restoreTerminal()
	if keys != nil {
		var cancelFollow context.CancelFunc
		followCtx, cancelFollow = context.WithCancel(sessionCtx)
		defer cancelFollow()
		go func() {
			select {
			case <-keys:
				detached.Store(true)
				cancelFollow()
			case <-followCtx.Done():
			}
		}()
		fmt.Fprintln(out,
			"\nfollowing logs; Ctrl-C pauses the project, d detaches and keeps it running")
	} else {
		fmt.Fprintln(out,
			"\nfollowing logs; Ctrl-C pauses the project (skali dev -d keeps it running)")
	}
	// Cluster lines interleave with child output through the mux; the pod
	// prefix printLogEvent writes already labels them.
	followOut := out
	if mux != nil {
		followOut = mux.Writer("")
	}
	if err := followRuntimeLogs(followCtx, followOut, api, environmentID, ""); err != nil {
		children.Terminate(childGrace)
		return err
	}
	// A dead session context wins over a simultaneous keypress: the user's
	// Ctrl-C asked for the pause. Children terminate on both paths.
	children.Terminate(childGrace)
	if detached.Load() && sessionCtx.Err() == nil {
		style := clirender.StyleFor(out)
		fmt.Fprintf(out, "\n%sdetached; the project keeps running\n", style.Check())
		if children != nil {
			fmt.Fprintln(out, "  local dev processes stopped; apps with dev blocks are down until the next skali dev")
		}
		fmt.Fprintf(out, "  %s  skali dev\n", style.Dim("reattach"))
		fmt.Fprintf(out, "  %s     skali dev down\n", style.Dim("pause"))
		return nil
	}
	return finishInterrupted(command, window.Load().(string), false)
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
		return errors.New("recent authentication required; run skali dev up first")
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

func isDeploymentInFlight(err error) bool {
	var apiErr *client.APIError
	return errors.As(err, &apiErr) && apiErr.Code == "deployment_in_flight"
}

// isRunAlreadyFinished matches the cancel endpoint's refusal to cancel a
// terminal run; for --force the slot is free either way.
func isRunAlreadyFinished(err error) bool {
	var apiErr *client.APIError
	return errors.As(err, &apiErr) && apiErr.Status == 409 && apiErr.Code == "conflict"
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
	localRemote := cfg.Remotes[localRemoteName]
	if localRemote == nil {
		return errors.New("the local platform is not set up; run skali dev up first")
	}
	api := remoteClient(cfg, localRemote)
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
					active = utils.ShortChecksum(status.ActiveRevision.Checksum)
				}
			} else {
				fmt.Fprintf(out, "  %s\n", style.Yellow(fmt.Sprintf(
					"warning: could not fetch status of %s/%s: %v", project.Name, environment.Name, err)))
			}
			fmt.Fprintf(out, "%-24s  %-13s  %s  %s\n", project.Name, environment.Name,
				stateColor(style, fmt.Sprintf("%-10s", state)), active)
		}
	}
	return nil
}

// ensureLocalPlatform brings the platform up and logs the CLI into it,
// storing the local remote.
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
	// A released CLI ahead of the platform names the gap once per session;
	// nothing here changes versions (that stays skali dev upgrade's job).
	if hint := upgradeHint(state.SkalidImage); hint != "" {
		fmt.Fprintln(out, clirender.StyleFor(out).Yellow(hint))
	}
	if err := loginLocalRemote(ctx, state); err != nil {
		return nil, err
	}
	return state, nil
}

// defaultSkalidImage prefers the recorded image, then a working-tree build
// when the CLI runs inside the repository (the developer path), then the
// published image matching a released binary's version.
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
	if releaseVersionPattern.MatchString(versionpkg.Version) {
		image := publishedSkalidRepo + versionpkg.Version
		task := tasks.Start("Pull " + image)
		if err := localdev.EnsureHostImage(ctx, image); err == nil {
			task.Done("")
			return image
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

// loginLocalRemote authenticates against the local installation with the
// recorded bootstrap credentials and stores the local remote.
func loginLocalRemote(ctx context.Context, state *localdev.State) error {
	cfg, err := cliconfig.Load()
	if err != nil {
		return err
	}
	// The local platform is owned by skali dev, so its identity is always
	// trusted: both paths re-pin whatever the platform answers with, and a
	// recreated platform (invalid token) simply falls through to a fresh
	// login instead of a mismatch prompt.
	existing := cfg.Remotes[localRemoteName]
	if existing != nil && existing.Token != "" {
		probe := client.New(localdev.MasterURL(), existing.Token, userAgent())
		if _, err := probe.CurrentSession(ctx); err == nil {
			if observed := probe.ObservedInstance(); observed != "" {
				existing.Instance = observed
			}
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
	if cfg.Remotes == nil {
		cfg.Remotes = map[string]*cliconfig.Remote{}
	}
	cfg.Remotes[localRemoteName] = &cliconfig.Remote{
		Master:   localdev.MasterURL(),
		Token:    result.Session.Token,
		Instance: api.ObservedInstance(),
	}
	return cliconfig.Save(cfg)
}

// localProjectEnvironment resolves the current project's local environment
// through the local remote.
func localProjectEnvironment(command *cobra.Command) (*client.Client, string, error) {
	project, err := loadLocalProject("")
	if err != nil {
		return nil, "", err
	}
	cfg, err := cliconfig.Load()
	if err != nil {
		return nil, "", err
	}
	localRemote := cfg.Remotes[localRemoteName]
	if localRemote == nil {
		return nil, "", errors.New("the local platform is not set up; run skali dev up first")
	}
	api := remoteClient(cfg, localRemote)
	_, environmentID, err := resolveEnvironmentIDs(command.Context(), api,
		project.Result.Definition.Name, localEnvironmentName)
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
		active = utils.ShortChecksum(status.ActiveRevision.Checksum)
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
		for _, route := range service.Routes {
			fmt.Fprintf(out, "    %s\n", routeLine(style, route))
		}
		for _, diagnostic := range service.Diagnostics {
			fmt.Fprintf(out, "    %s %s\n",
				severityColor(style, diagnostic.Severity+":"), diagnostic.Message)
		}
	}
	return nil
}

// routeLine renders one public route: its URL, a non-default strategy, and
// the certificate state on TLS-capable installations. Local platforms have
// no certificates, so the line stays a bare http URL.
func routeLine(style *clirender.Style, route client.RouteStatus) string {
	scheme := "http"
	if route.Certificate != nil {
		scheme = "https"
	}
	line := scheme + "://" + route.Domain
	if route.Path != "" && route.Path != "/" {
		line += route.Path
	}
	if route.Strategy == "least-requests" {
		line += " (least-requests)"
	}
	if certificate := route.Certificate; certificate != nil {
		detail := stateColor(style, certificate.State)
		if certificate.State != "active" && certificate.Reason != "" {
			detail += " (" + certificate.Reason + ")"
		}
		line += "  cert " + detail
	}
	return line
}

// stateColor paints a lifecycle word by its meaning; padding around the
// word survives because the switch trims before matching.
func stateColor(style *clirender.Style, state string) string {
	switch strings.TrimSpace(state) {
	case "running", "healthy", "active", "succeeded", "ready":
		return style.Green(state)
	case "stopped", "down", "releasing", "progressing", "degraded", "waiting", "pending", "issuing":
		return style.Yellow(state)
	case "failed", "unhealthy", "error", "cancelled", "failing", "expired":
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
		confirmed, err := cliprompt.New(os.Stdin, out).Confirm(ctx, cliprompt.ConfirmOptions{
			Title: "Destroy the local installation?",
		})
		if err != nil {
			return err
		}
		if !confirmed {
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

	// Drop the stored local remote; its token died with the cluster.
	if cfg, err := cliconfig.Load(); err == nil {
		delete(cfg.Remotes, localRemoteName)
		_ = cliconfig.Save(cfg)
	}
	return nil
}

// printDevReady prints the local ready summary: the dashboard, every
// route as a clickable localhost URL, and the host dev process behind an
// intercepted application.
func printDevReady(command *cobra.Command, api *client.Client, environmentID string,
	devPorts map[string]map[string]int) {
	printReadySummary(command.Context(), command.OutOrStdout(), api, environmentID, readySummary{
		Dashboard: localdev.MasterURL(),
		HTTPPort:  localdev.HTTPPort(),
		DevPorts:  devPorts,
	})
}
