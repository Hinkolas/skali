package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Hinkolas/skali/internal/build"
	"github.com/Hinkolas/skali/internal/checkout"
	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/manifest"
	"github.com/Hinkolas/skali/internal/values"
)

// deployOptions are the shared knobs of skali plan, deploy, and dev.
type deployOptions struct {
	Environment string
	Manifest    string
	EnvFile     string
	BuildMode   string
	// From promotes another environment's active revision instead of the
	// local checkout; no manifest is read and nothing builds.
	From             string
	Yes              bool
	AllowDestructive bool
	Detach           bool
	// Force deploys even when the environment is up to date; application
	// workloads restart at promotion. Data is never touched.
	Force bool
	// Rebuild ignores artifact reuse and disables build caches so moved
	// upstream tags and refreshed base images are picked up.
	Rebuild bool
	// Platform overrides the build platform(s); empty follows the
	// server-reported cluster architecture.
	Platform string
	// AutoEnvFile uses ./.env automatically when present and offers the
	// discovered env files otherwise (bare dev).
	AutoEnvFile bool
	// CreateMissing provisions the project and environment through the API
	// when absent (local dev); remote deploys create only interactively,
	// behind explicit confirmation.
	CreateMissing bool
	// UseBinding reads and writes the .skali/ checkout binding; set by
	// plan and deploy. dev force-selects the local remote and never
	// touches the binding.
	UseBinding bool
	// OnDeploymentOpened and OnDeploymentClosed observe the artifact
	// window so a signaled dev session can fail an interrupted window on a
	// fresh context (pause-on-exit); both optional.
	OnDeploymentOpened func(deploymentID string)
	OnDeploymentClosed func()
}

// project bundles everything the flow knows about the local checkout.
type localProject struct {
	Path   string // manifest path
	Root   string // project root (manifest directory)
	Source []byte
	Result *compiler.Result
}

func loadLocalProject(explicit string) (*localProject, error) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}
	path, err := manifest.Discover(explicit, workingDirectory)
	if err != nil {
		return nil, err
	}
	source, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	document, err := manifest.ParseFile(path)
	if err != nil {
		return nil, err
	}
	result, err := compiler.Compile(document)
	if err != nil {
		return nil, err
	}
	return &localProject{
		Path:   path,
		Root:   filepath.Dir(path),
		Source: source,
		Result: result,
	}, nil
}

// findProject lists the installation's projects once and returns the
// named one, or nil when absent.
func findProject(ctx context.Context, api *client.Client, name string) (*client.Project, error) {
	projects, err := api.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	for i := range projects {
		if projects[i].Name == name {
			return &projects[i], nil
		}
	}
	return nil, nil
}

// findEnvironment returns the named environment among the given, or nil.
func findEnvironment(environments []client.Environment, name string) *client.Environment {
	for i := range environments {
		if environments[i].Name == name {
			return &environments[i]
		}
	}
	return nil
}

// resolveEnvironmentIDs finds the project and environment on the target
// installation; both must exist.
func resolveEnvironmentIDs(ctx context.Context, api *client.Client, projectName, environmentName string) (projectID, environmentID string, err error) {
	project, err := findProject(ctx, api, projectName)
	if err != nil {
		return "", "", err
	}
	if project == nil {
		return "", "", fmt.Errorf("project %s does not exist on this installation", projectName)
	}
	environments, err := api.ListEnvironments(ctx, project.ID)
	if err != nil {
		return "", "", err
	}
	environment := findEnvironment(environments, environmentName)
	if environment == nil {
		return "", "", fmt.Errorf("environment %s does not exist in project %s", environmentName, projectName)
	}
	return project.ID, environment.ID, nil
}

// sameMaster reports whether two master URLs identify the same
// installation: folded scheme and host, identical path after trailing
// slashes are trimmed. Scheme and port differences are distinct
// installations by design.
func sameMaster(a, b string) bool {
	trimmedA := strings.TrimRight(strings.TrimSpace(a), "/")
	trimmedB := strings.TrimRight(strings.TrimSpace(b), "/")
	parsedA, errA := url.Parse(trimmedA)
	parsedB, errB := url.Parse(trimmedB)
	if errA != nil || errB != nil {
		return strings.EqualFold(trimmedA, trimmedB)
	}
	return strings.EqualFold(parsedA.Scheme, parsedB.Scheme) &&
		strings.EqualFold(parsedA.Host, parsedB.Host) &&
		parsedA.Path == parsedB.Path
}

// lookupRemoteByMaster finds the remote whose master URL matches, scanning
// names in sorted order so duplicates resolve deterministically.
func lookupRemoteByMaster(cfg *cliconfig.Config, master string) (string, *cliconfig.Remote, bool) {
	names := make([]string, 0, len(cfg.Remotes))
	for name := range cfg.Remotes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if remote := cfg.Remotes[name]; remote != nil && sameMaster(remote.Master, master) {
			return name, remote, true
		}
	}
	return "", nil, false
}

// selectValues decides the value source: an explicit --env-file, the bare-dev
// automatic ./.env, an interactively selected override from the project
// root's env files, or nil for the environment's stored values (the default).
func selectValues(out io.Writer, project *localProject, opts *deployOptions) (*values.File, error) {
	path := opts.EnvFile
	if path == "" {
		prompt := cliprompt.Interactive() && !opts.Yes
		if opts.AutoEnvFile {
			candidate := filepath.Join(project.Root, ".env")
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				path = candidate
			}
			// Without a ./.env to default to, dev offers the discovered env
			// files like deploy does instead of silently falling back to
			// the stored values; its implied --yes only skips confirmations.
			prompt = path == "" && cliprompt.Interactive()
		}
		if path == "" {
			if !prompt {
				return nil, nil
			}
			selected, err := chooseEnvFile(out, bufio.NewReader(os.Stdin), project.Root, opts.Environment)
			if err != nil {
				return nil, err
			}
			if selected == "" {
				return nil, nil
			}
			path = selected
		}
	}
	parsed, err := values.ParseFile(path)
	if err != nil {
		return nil, err
	}
	return parsed, nil
}

// discoverEnvFiles lists the project root's .env and .env.* files, .env
// first, for the interactive override selection.
func discoverEnvFiles(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == ".env" || strings.HasPrefix(name, ".env.") {
			files = append(files, filepath.Join(root, name))
		}
	}
	sort.Strings(files)
	return files
}

// chooseEnvFile offers the discovered env files as an override for the
// environment's stored values; an empty result keeps the stored values.
func chooseEnvFile(out io.Writer, in *bufio.Reader, root, environment string) (string, error) {
	files := discoverEnvFiles(root)
	if len(files) == 0 {
		return "", nil
	}
	options := make([]cliprompt.Option, 0, len(files)+1)
	options = append(options, cliprompt.Option{
		Label: "Use stored values",
		Value: "",
	})
	for _, file := range files {
		options = append(options, cliprompt.Option{
			Label: filepath.Base(file),
			Value: file,
		})
	}
	return promptSession(out, in).Select(context.Background(), cliprompt.SelectOptions{
		Title:       fmt.Sprintf("Override %s with a local env file?", environment),
		Description: "Stored environment values remain the default.",
		Options:     options,
	})
}

// chooseEnvironment asks for one of the project's environments; a single
// environment selects itself. The caller guarantees at least one.
func chooseEnvironment(out io.Writer, in *bufio.Reader, environments []client.Environment) (string, error) {
	if len(environments) == 1 {
		return environments[0].Name, nil
	}
	options := make([]cliprompt.Option, 0, len(environments))
	for _, environment := range environments {
		options = append(options, cliprompt.Option{
			Label: environment.Name,
			Value: environment.Name,
		})
	}
	return promptSession(out, in).Select(context.Background(), cliprompt.SelectOptions{
		Title:   "Which environment should Skali use?",
		Options: options,
	})
}

// resolveBuildPlatform picks the platform local builds target: an explicit
// override wins, then the platforms the server observed on the cluster
// nodes, then the host architecture when the server reports nothing (older
// servers, observation not yet synced). Multiple platforms join into one
// comma-separated multi-platform build.
func resolveBuildPlatform(out io.Writer, status *client.EnvironmentStatus, override string) string {
	style := clirender.StyleFor(out)
	local := "linux/" + runtime.GOARCH
	if override != "" {
		platform := canonicalPlatforms(strings.Split(override, ","))
		fmt.Fprintf(out, "%s     %s %s\n", style.Dim("platform"), platform, style.Dim("(override)"))
		return platform
	}
	if status != nil && len(status.Platforms) > 0 {
		platform := canonicalPlatforms(status.Platforms)
		if platform != local {
			fmt.Fprintf(out, "%s     %s %s\n", style.Dim("platform"), platform, style.Dim("(cluster architecture)"))
		}
		return platform
	}
	fmt.Fprintf(out, "%s     %s %s\n", style.Dim("platform"), local,
		style.Dim("(local architecture; the server did not report cluster platforms)"))
	return local
}

// canonicalPlatforms trims, dedupes, and sorts so that equal platform sets
// produce equal strings and therefore equal input hashes.
func canonicalPlatforms(platforms []string) string {
	seen := make(map[string]struct{}, len(platforms))
	cleaned := make([]string, 0, len(platforms))
	for _, platform := range platforms {
		platform = strings.TrimSpace(platform)
		if platform == "" {
			continue
		}
		if _, ok := seen[platform]; ok {
			continue
		}
		seen[platform] = struct{}{}
		cleaned = append(cleaned, platform)
	}
	sort.Strings(cleaned)
	return strings.Join(cleaned, ",")
}

// buildInputs computes the per-application hashes: the dedup key the
// server decides reuse with. Selected env files never enter the context.
func buildInputs(project *localProject, excludeFiles []string, platform string) (map[string]client.BuildInput, map[string]*build.Context, error) {
	inputs := make(map[string]client.BuildInput)
	contexts := make(map[string]*build.Context)
	for key, application := range project.Result.Definition.Applications {
		if application.Source.Kind != "build" {
			continue
		}
		spec := application.Source.Build
		collected, err := build.Collect(project.Root, spec.Context, build.CollectOptions{ExcludeFiles: excludeFiles})
		if err != nil {
			return nil, nil, fmt.Errorf("collect build context for %s: %w", key, err)
		}
		dockerfile, err := os.ReadFile(filepath.Join(project.Root, spec.Dockerfile))
		if err != nil {
			return nil, nil, fmt.Errorf("read Dockerfile for %s: %w", key, err)
		}
		configHash := build.ConfigHash(dockerfile, spec.Target, spec.Arguments)
		inputs[key] = client.BuildInput{
			InputHash:  build.InputHash(collected.TreeHash, configHash, platform),
			ConfigHash: configHash,
			Platform:   platform,
		}
		contexts[key] = collected
	}
	return inputs, contexts, nil
}

func printPlan(out io.Writer, plan *client.PlanDocument, actions []client.ArtifactAction, activeChecksum string) {
	style := clirender.StyleFor(out)
	if activeChecksum != "" {
		fmt.Fprintf(out, "\nplan against active revision %s\n", shortChecksum(activeChecksum))
	} else {
		fmt.Fprintf(out, "\nplan for the initial deployment\n")
	}
	rebuilt := make(map[string]string, len(actions))
	for _, action := range actions {
		rebuilt[action.Application] = action.Action
	}
	for _, change := range plan.Changes {
		var reasons []string
		if change.Detail != "" {
			reasons = strings.Split(change.Detail, "; ")
		}
		if action, ok := rebuilt[strings.TrimPrefix(change.Service, "applications.")]; ok && change.Action != "remove" {
			switch action {
			case "build":
				reasons = append(reasons, "artifact will be rebuilt")
			case "import":
				reasons = append(reasons, "image will be imported")
			case "reuse":
				reasons = append(reasons, "artifact reused")
			}
		}
		first, rest := "", []string(nil)
		if len(reasons) > 0 {
			first, rest = reasons[0], reasons[1:]
		}
		if change.Destructive {
			first = style.BoldRed("DESTRUCTIVE:") + " " + first
		}
		fmt.Fprintf(out, "  %s %-24s %s\n", actionColor(style, change.Action), change.Service, first)
		for _, reason := range rest {
			fmt.Fprintf(out, "  %7s %-24s %s\n", "", "", reason)
		}
	}
	for _, value := range plan.Values {
		fmt.Fprintf(out, "  %s %-24s %s\n", actionColor(style, "value"), value.Name, value.Action)
	}
	if plan.Empty() {
		fmt.Fprintln(out, "  "+style.Dim("no changes"))
	} else if !plan.Destructive() {
		fmt.Fprintln(out, "\n"+style.Dim("no destructive changes"))
	}
}

// healthHints names the applications whose compiled definition declares no
// readiness probe: their rollouts can only verify that pods run, so the
// rollout health guarantee is weak. Advisory only, never an error.
func healthHints(result *compiler.Result) []string {
	var hints []string
	for _, key := range sortedApplicationKeys(result.Definition.Applications) {
		if result.Definition.Applications[key].Health.Readiness.HTTP.Path == "" {
			hints = append(hints,
				"hint: application "+key+" declares no health check; rollouts cannot verify readiness")
		}
	}
	return hints
}

func sortedApplicationKeys(applications map[string]compiler.Application) []string {
	keys := make([]string, 0, len(applications))
	for key := range applications {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// printHealthHints renders the advisory block after a plan.
func printHealthHints(out io.Writer, result *compiler.Result) {
	hints := healthHints(result)
	if len(hints) == 0 {
		return
	}
	style := clirender.StyleFor(out)
	fmt.Fprintln(out)
	for _, hint := range hints {
		fmt.Fprintln(out, "  "+style.Dim(hint))
	}
}

// actionColor paints a plan action word in its own column: additions
// green, removals red, everything else neutral.
func actionColor(style *clirender.Style, action string) string {
	padded := fmt.Sprintf("%-7s", action)
	switch action {
	case "create":
		return style.Green(padded)
	case "remove", "delete", "destroy":
		return style.Red(padded)
	case "update", "replace":
		return style.Yellow(padded)
	case "value":
		return style.Dim(padded)
	}
	return padded
}

// confirmDestructive requires the environment name typed back.
func confirmDestructive(ctx context.Context, out io.Writer, environment string) (bool, error) {
	return cliprompt.New(os.Stdin, out).ConfirmTyped(ctx,
		"This plan is destructive",
		fmt.Sprintf("Type %q exactly to continue.", environment),
		environment)
}

// confirmPlan applies the deploy confirmation policy: destructive plans
// demand the typed environment name or the explicit flag, ordinary plans a
// simple yes. It flips request.AllowDestructive once confirmed.
func confirmPlan(ctx context.Context, out io.Writer, opts *deployOptions,
	planned *client.PlanResult, request *client.DeployRequest) error {
	if planned.Plan.Destructive() && !opts.AllowDestructive {
		if cliprompt.Interactive() && !opts.Yes {
			confirmed, err := confirmDestructive(ctx, out, opts.Environment)
			if err != nil {
				return err
			}
			if !confirmed {
				return errors.New("aborted")
			}
			request.AllowDestructive = true
		} else {
			return errors.New("plan is destructive; review it and re-run with --allow-destructive")
		}
	} else if planned.Plan.Destructive() {
		request.AllowDestructive = true
	} else if !opts.Yes {
		if !cliprompt.Interactive() {
			return errors.New("non-interactive use requires --yes")
		}
		confirmed, err := cliprompt.New(os.Stdin, out).Confirm(ctx, cliprompt.ConfirmOptions{
			Title: "Continue with this deployment?",
		})
		if err != nil {
			return err
		}
		if !confirmed {
			return errors.New("aborted")
		}
	}
	return nil
}

// stepLogSink batches engine progress into the journal's client surface;
// the lock serializes the build engine's stdout and stderr streams.
type stepLogSink struct {
	ctx    context.Context
	api    *client.Client
	stepID string

	mu     sync.Mutex
	buffer []client.LogLine
}

func (s *stepLogSink) Line(level, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buffer = append(s.buffer, client.LogLine{Level: level, Message: message})
	if len(s.buffer) >= 25 {
		s.flushLocked()
	}
}

func (s *stepLogSink) Flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushLocked()
}

func (s *stepLogSink) flushLocked() {
	if len(s.buffer) == 0 {
		return
	}
	_ = s.api.AppendStepLogs(s.ctx, s.stepID, s.buffer)
	s.buffer = nil
}

// teeSink forwards journal lines to the server and mirrors them into the
// live task display, so builds and pulls show their progress locally.
type teeSink struct {
	inner *stepLogSink
	task  *clirender.Task
}

func (t *teeSink) Line(level, message string) {
	t.inner.Line(level, message)
	t.task.Note(message)
}

// registryUsername is the Basic username presented to the registry token
// endpoint. It is cosmetic: the endpoint authenticates the password (a
// session token) and derives the subject from the session, ignoring the
// username entirely.
const registryUsername = "skali-session"

// executeActions performs the client side of the artifact window: builds
// and imports with journaled steps, heartbeats, and server verification.
// registryAuth authenticates every managed-registry push in-process; nil
// pushes anonymously (the local registry never challenges).
func executeActions(ctx context.Context, out io.Writer, api *client.Client,
	opened *client.OpenedDeployment, project *localProject, contexts map[string]*build.Context,
	registryAuth authn.Authenticator, rebuild bool) error {

	engine := &build.Docker{Auth: registryAuth}
	tasks := clirender.NewTasks(out)
	runID := opened.Deployment.RunID
	for _, action := range opened.Actions {
		switch action.Action {
		case "reuse":
			tasks.Start("artifact for " + action.Application).
				Skip("current, " + shortChecksum(action.Digest))
			continue
		case "build":
			if _, err := api.EnsureStep(ctx, runID, action.StepKey, action.Application, "artifacts"); err != nil {
				return err
			}
			application := project.Result.Definition.Applications[action.Application]
			spec := application.Source.Build
			buildStep, err := api.EnsureStep(ctx, runID, action.StepKey+".build",
				"Build locally ("+action.Platform+")", action.StepKey)
			if err != nil {
				return err
			}
			if err := api.SetStepStatus(ctx, buildStep.ID, "running"); err != nil {
				return err
			}
			stopHeartbeat := startHeartbeat(ctx, api, action.BuildID)
			task := tasks.Start(fmt.Sprintf("Build %s (%s)", action.Application, action.Platform))
			sink := &stepLogSink{ctx: ctx, api: api, stepID: buildStep.ID}
			result, err := engine.Build(ctx, build.BuildRequest{
				ContextDir: contexts[action.Application].Dir,
				Dockerfile: filepath.Join(project.Root, spec.Dockerfile),
				Target:     spec.Target,
				Arguments:  spec.Arguments,
				Platform:   action.Platform,
				PushRef:    action.PushRef,
				Rebuild:    rebuild,
			}, &teeSink{inner: sink, task: task})
			sink.Flush()
			stopHeartbeat()
			if err != nil {
				task.Fail()
				_ = api.SetStepStatus(ctx, buildStep.ID, "failed")
				return failDeployment(ctx, api, opened, fmt.Errorf("build for %s failed: %w", action.Application, err))
			}
			task.Done(shortChecksum(result.Digest))
			if err := api.SetStepStatus(ctx, buildStep.ID, "succeeded"); err != nil {
				return err
			}
			if err := verifyAction(ctx, api, opened, action, result.Digest); err != nil {
				return err
			}
		case "import":
			if _, err := api.EnsureStep(ctx, runID, action.StepKey, action.Application, "artifacts"); err != nil {
				return err
			}
			importStep, err := api.EnsureStep(ctx, runID, action.StepKey+".import",
				"Import "+action.Upstream, action.StepKey)
			if err != nil {
				return err
			}
			if err := api.SetStepStatus(ctx, importStep.ID, "running"); err != nil {
				return err
			}
			task := tasks.Start("Import " + action.Upstream)
			sink := &stepLogSink{ctx: ctx, api: api, stepID: importStep.ID}
			result, err := build.Import(ctx, action.Upstream, action.PushRef,
				build.ImportOptions{TargetAuth: registryAuth}, &teeSink{inner: sink, task: task})
			sink.Flush()
			if err != nil {
				task.Fail()
				_ = api.SetStepStatus(ctx, importStep.ID, "failed")
				return failDeployment(ctx, api, opened, fmt.Errorf("import for %s failed: %w", action.Application, err))
			}
			task.Done(shortChecksum(result.Digest))
			if err := api.SetStepStatus(ctx, importStep.ID, "succeeded"); err != nil {
				return err
			}
			if err := verifyAction(ctx, api, opened, action, result.Digest); err != nil {
				return err
			}
		}
	}
	return nil
}

// verifyAction journals the verification step around the server's registry
// check.
func verifyAction(ctx context.Context, api *client.Client, opened *client.OpenedDeployment,
	action client.ArtifactAction, digest string) error {
	step, err := api.EnsureStep(ctx, opened.Deployment.RunID, action.StepKey+".verify",
		"Verify "+shortChecksum(digest), action.StepKey)
	if err != nil {
		return err
	}
	if err := api.SetStepStatus(ctx, step.ID, "running"); err != nil {
		return err
	}
	if _, err := api.VerifyArtifact(ctx, action.ArtifactID, opened.Deployment.ID, digest); err != nil {
		_ = api.SetStepStatus(ctx, step.ID, "failed")
		return failDeployment(ctx, api, opened, fmt.Errorf("verify %s: %w", action.Application, err))
	}
	return api.SetStepStatus(ctx, step.ID, "succeeded")
}

// failDeployment reports the client-side failure and returns the original
// error; values, target, and active revision stay untouched server-side.
func failDeployment(ctx context.Context, api *client.Client, opened *client.OpenedDeployment, cause error) error {
	if err := api.FailDeployment(ctx, opened.Deployment.ID); err != nil {
		return fmt.Errorf("%w (additionally, failing the deployment: %v)", cause, err)
	}
	return cause
}

func startHeartbeat(ctx context.Context, api *client.Client, buildID string) (stop func()) {
	heartbeatCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				_, _ = api.HeartbeatBuild(heartbeatCtx, buildID)
			}
		}
	}()
	return func() { cancel(); <-done }
}

// attachRun renders the run tree until it settles or the user detaches
// with an interrupt (detaching never cancels). A parent context dying under
// the wait (a deadline, a lost session) is not a detach: nobody asked for
// one, so it returns "interrupted" without claiming anything.
func attachRun(ctx context.Context, out io.Writer, api *client.Client, runID string) (string, error) {
	attachCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	tty := term.IsTerminal(int(os.Stdout.Fd()))
	renderer := &clirender.Renderer{
		Out:   out,
		TTY:   tty,
		Style: clirender.StyleFor(out),
		Logs: func(stepID string) []string {
			logs, _, err := api.StepLogs(ctx, stepID, "", 0)
			if err != nil || len(logs) == 0 {
				return nil
			}
			tail := logs
			if len(tail) > 3 {
				tail = tail[len(tail)-3:]
			}
			lines := make([]string, len(tail))
			for index, entry := range tail {
				lines[index] = entry.Message
			}
			return lines
		},
	}

	poll := time.NewTicker(500 * time.Millisecond)
	defer poll.Stop()
	// The spinner ticks faster than the server poll so the display stays
	// visibly alive between snapshots; without a TTY nothing animates.
	spin := time.NewTicker(150 * time.Millisecond)
	defer spin.Stop()
	if !tty {
		spin.Stop()
	}

	kind := "run"
	for {
		tree, err := api.GetRun(ctx, runID)
		if err != nil {
			if ctx.Err() != nil {
				return "interrupted", nil
			}
			return "", err
		}
		if tree.Run.Kind != "" {
			kind = tree.Run.Kind
		}
		renderer.Render(tree)
		switch tree.Run.Status {
		case "succeeded", "failed", "cancelled":
			return tree.Run.Status, nil
		}
		for waiting := true; waiting; {
			select {
			case <-attachCtx.Done():
				renderer.Detach()
				if ctx.Err() != nil {
					return "interrupted", nil
				}
				fmt.Fprintf(out, "\ndetached from run %s; the %s continues on the server\n", runID, kind)
				fmt.Fprintf(out, "  reattach  skali run attach %s\n", runID)
				return "detached", nil
			case <-spin.C:
				renderer.Tick()
			case <-poll.C:
				waiting = false
			}
		}
	}
}

// runDeployFlow is the transcript loop shared by skali plan, deploy, and
// dev. planOnly stops after printing the server-computed plan.
// Deploy flow outcomes: what actually happened, so callers like bare
// skali dev can decide whether following runtime logs is welcome (a user
// who already detached with Ctrl-C is not asking for more output).
const (
	deployOutcomePlanned  = "planned"
	deployOutcomeUpToDate = "up-to-date"
	deployOutcomeReady    = "ready"
	deployOutcomeDetached = "detached"
)

// deployTarget is the resolved destination of one plan, deploy, or dev
// invocation.
type deployTarget struct {
	remoteName    string
	master        string
	api           *client.Client
	projectID     string
	environmentID string
	// sessionToken doubles as the managed-registry push credential: the
	// registry token endpoint accepts it as the Basic password, so builds
	// and imports authenticate without any docker login.
	sessionToken string
}

// resolveDeployTarget resolves remote, project, and environment, creating
// project and environment per policy (interactive deploy behind explicit
// confirmation, dev silently, plan and non-interactive never), and links
// the checkout on success. prompts is cliprompt.Interactive() && !opts.Yes.
func resolveDeployTarget(ctx context.Context, out io.Writer, in *bufio.Reader,
	project *localProject, opts *deployOptions, planOnly, prompts bool) (*deployTarget, error) {

	style := clirender.StyleFor(out)
	projectName := project.Result.Definition.Name

	cfg, err := cliconfig.Load()
	if err != nil {
		return nil, err
	}
	var binding *checkout.Target
	if opts.UseBinding {
		if binding, err = checkout.Load(project.Root); err != nil {
			return nil, err
		}
	}
	if binding != nil && binding.Project != projectName {
		return nil, fmt.Errorf("this checkout is linked to project %s but the manifest names %s; "+
			"fix the manifest name or delete .skali/target.yaml to relink", binding.Project, projectName)
	}

	var remoteName string
	var remote *cliconfig.Remote
	if binding != nil {
		name, found, ok := lookupRemoteByMaster(cfg, binding.Master)
		if !ok {
			return nil, fmt.Errorf("no remote for %s on this machine; run skali remote add %s",
				binding.Master, binding.Master)
		}
		remoteName, remote = name, found
	} else {
		if remoteName, remote, err = cfg.Current(); err != nil {
			return nil, err
		}
	}
	api := client.New(remote.Master, remote.Token, userAgent())

	if opts.UseBinding {
		fmt.Fprintf(out, "%s       %s %s\n", style.Dim("remote"), remoteName,
			style.Dim("("+remote.Master+")"))
	}
	fmt.Fprintf(out, "%s      %s %s\n", style.Dim("project"),
		projectName, style.Dim("("+filepath.Base(project.Path)+")"))

	// The bound environment is the default; --environment overrides it for
	// one invocation without rewriting the binding.
	if opts.Environment == "" && binding != nil {
		opts.Environment = binding.Environment
	}

	proj, err := findProject(ctx, api, projectName)
	if err != nil {
		return nil, err
	}
	projectID := ""
	switch {
	case proj != nil:
		projectID = proj.ID
	case planOnly:
		return nil, fmt.Errorf("project %s does not exist on %s; "+
			"skali plan never changes the installation, run skali deploy to create it",
			projectName, remote.Master)
	case opts.CreateMissing:
		created, err := api.CreateProject(ctx, projectName)
		if err != nil {
			return nil, err
		}
		projectID = created.ID
	case !prompts:
		return nil, fmt.Errorf("project %s does not exist on %s; "+
			"run skali deploy interactively to create it", projectName, remote.Master)
	default:
		confirmed, err := promptSession(out, in).Confirm(ctx, cliprompt.ConfirmOptions{
			Title: fmt.Sprintf("Create project %s on %s?", projectName, remoteName),
		})
		if err != nil {
			return nil, err
		}
		if !confirmed {
			return nil, errors.New("aborted")
		}
		created, err := api.CreateProject(ctx, projectName)
		if err != nil {
			return nil, err
		}
		projectID = created.ID
	}

	environmentID, err := resolveEnvironmentTarget(ctx, out, in, api,
		projectID, projectName, remote.Master, opts, planOnly, prompts)
	if err != nil {
		return nil, err
	}

	// Link the checkout on first contact. The dev-owned local remote is
	// disposable and never bound.
	if opts.UseBinding && binding == nil && remoteName != localRemoteName {
		if err := checkout.Save(project.Root, &checkout.Target{
			Master:      remote.Master,
			Project:     projectName,
			Environment: opts.Environment,
		}); err != nil {
			return nil, err
		}
		fmt.Fprintln(out, style.Dim(fmt.Sprintf(
			"linked to remote %s, project %s, environment %s; stored in .skali/",
			remoteName, projectName, opts.Environment)))
	}

	return &deployTarget{
		remoteName:    remoteName,
		master:        remote.Master,
		api:           api,
		projectID:     projectID,
		environmentID: environmentID,
		sessionToken:  remote.Token,
	}, nil
}

// resolveEnvironmentTarget resolves or creates (per the same policy as the
// project: interactive deploy behind explicit confirmation, dev silently,
// plan and non-interactive never) the environment named by opts.Environment,
// prompting for a name when empty. It prints the environment line and
// returns the environment id.
func resolveEnvironmentTarget(ctx context.Context, out io.Writer, in *bufio.Reader, api *client.Client,
	projectID, projectName, master string, opts *deployOptions, planOnly, prompts bool) (string, error) {

	style := clirender.StyleFor(out)
	environments, err := api.ListEnvironments(ctx, projectID)
	if err != nil {
		return "", err
	}
	if opts.Environment == "" {
		switch {
		case !prompts:
			return "", errors.New("--environment is required")
		case len(environments) == 0 && planOnly:
			return "", fmt.Errorf("project %s has no environments on %s; run skali deploy to create one",
				projectName, master)
		case len(environments) == 0:
			opts.Environment, err = promptSession(out, in).Text(ctx, cliprompt.TextOptions{
				Title:   "Environment name",
				Default: "production",
				Validate: func(value string) error {
					if value == "" {
						return errors.New("environment name is required")
					}
					return nil
				},
			})
			if err != nil {
				return "", err
			}
		default:
			environment, err := chooseEnvironment(out, in, environments)
			if err != nil {
				return "", err
			}
			opts.Environment = environment
		}
	}

	environment := findEnvironment(environments, opts.Environment)
	environmentID := ""
	switch {
	case environment != nil:
		environmentID = environment.ID
	case planOnly:
		return "", fmt.Errorf("environment %s does not exist in project %s on %s; "+
			"skali plan never changes the installation, run skali deploy to create it",
			opts.Environment, projectName, master)
	case opts.CreateMissing:
		created, err := api.CreateEnvironment(ctx, projectID, opts.Environment)
		if err != nil {
			return "", err
		}
		environmentID = created.ID
	case !prompts:
		return "", fmt.Errorf("environment %s does not exist in project %s on %s; "+
			"run skali deploy interactively to create it", opts.Environment, projectName, master)
	default:
		confirmed, err := promptSession(out, in).Confirm(ctx, cliprompt.ConfirmOptions{
			Title: fmt.Sprintf("Create environment %s in project %s?",
				opts.Environment, projectName),
		})
		if err != nil {
			return "", err
		}
		if !confirmed {
			return "", errors.New("aborted")
		}
		created, err := api.CreateEnvironment(ctx, projectID, opts.Environment)
		if err != nil {
			return "", err
		}
		environmentID = created.ID
	}

	fmt.Fprintf(out, "%s  %s\n", style.Dim("environment"), opts.Environment)
	return environmentID, nil
}

func runDeployFlow(command *cobra.Command, opts *deployOptions, planOnly bool) (string, error) {
	ctx := command.Context()
	out := command.OutOrStdout()
	project, err := loadLocalProject(opts.Manifest)
	if err != nil {
		return "", err
	}
	style := clirender.StyleFor(out)
	prompts := cliprompt.Interactive() && !opts.Yes
	target, err := resolveDeployTarget(ctx, out, bufio.NewReader(os.Stdin),
		project, opts, planOnly, prompts)
	if err != nil {
		return "", err
	}
	api, projectID, environmentID := target.api, target.projectID, target.environmentID
	definitionVersion, err := api.SubmitDefinition(ctx, projectID, string(project.Source), "yaml")
	if err != nil {
		return "", err
	}

	// Values: an explicit or discovered local file stages a candidate;
	// otherwise the environment's stored values apply.
	file, err := selectValues(out, project, opts)
	if err != nil {
		return "", err
	}
	candidateID := ""
	var excludeFiles []string
	if file != nil {
		kept, missing, skipped := values.Conform(project.Result.Definition.RequiredVariables, file.Values)
		if len(missing) > 0 {
			return "", fmt.Errorf("%s: missing required project values: %s", file.Path, strings.Join(missing, ", "))
		}
		if !planOnly {
			staged, err := api.StageValues(ctx, environmentID, kept, definitionVersion.DefinitionVersionID)
			if err != nil {
				return "", err
			}
			candidateID = staged.CandidateID
			fmt.Fprintf(out, "%s       %s %s\n", style.Dim("values"), file.Path,
				style.Dim(fmt.Sprintf("(%d staged)", len(staged.Staged))))
			if len(staged.Skipped) > 0 {
				skipped = staged.Skipped
			}
		} else {
			fmt.Fprintf(out, "%s       %s %s\n", style.Dim("values"), file.Path,
				style.Dim(fmt.Sprintf("(%d values; validated, not uploaded)", len(kept))))
		}
		if len(skipped) > 0 {
			fmt.Fprintf(out, "  %s\n", style.Yellow("warning: skipped keys not referenced by the manifest: "+
				strings.Join(skipped, ", ")))
		}
		excludeFiles = append(excludeFiles, file.Path)
	}

	// The environment status is fetched before hashing because it carries
	// the cluster's node platforms, which are part of every input hash.
	var envStatus *client.EnvironmentStatus
	if status, err := api.EnvironmentStatus(ctx, environmentID); err == nil {
		envStatus = status
	} else {
		fmt.Fprintf(out, "  %s\n", style.Yellow(fmt.Sprintf(
			"warning: could not fetch environment status: %v", err)))
	}
	platform := resolveBuildPlatform(out, envStatus, opts.Platform)
	inputs, contexts, err := buildInputs(project, excludeFiles, platform)
	if err != nil {
		return "", err
	}
	request := client.DeployRequest{
		DefinitionVersionID: definitionVersion.DefinitionVersionID,
		CandidateID:         candidateID,
		BuildExecutor:       "local",
		Builds:              inputs,
		Force:               opts.Force,
		Rebuild:             opts.Rebuild,
	}

	activeChecksum := ""
	if envStatus != nil && envStatus.ActiveRevision != nil {
		activeChecksum = envStatus.ActiveRevision.Checksum
	}
	planned, err := api.Plan(ctx, environmentID, request)
	if err != nil {
		return "", err
	}
	printPlan(out, planned.Plan, planned.Actions, activeChecksum)
	if len(planned.Orphaned) > 0 {
		fmt.Fprintf(out, "\n  %s\n", style.Yellow("warning: ignoring stored values no longer referenced by the manifest: "+
			strings.Join(planned.Orphaned, ", ")+" (remove them with skali values unset)"))
	}
	printHealthHints(out, project.Result)
	if planOnly {
		return deployOutcomePlanned, nil
	}
	if planned.UpToDate && !opts.Force {
		fmt.Fprintln(out, "\nnothing to deploy")
		return deployOutcomeUpToDate, nil
	}
	if planned.UpToDate {
		fmt.Fprintln(out, "\nnothing changed; deploying anyway (--force restarts the application workloads)")
	}

	if err := confirmPlan(ctx, out, opts, planned, &request); err != nil {
		return "", err
	}

	opened, err := api.OpenDeployment(ctx, environmentID, request)
	if err != nil {
		return "", err
	}
	if opened.UpToDate {
		fmt.Fprintln(out, "\nnothing to deploy")
		return deployOutcomeUpToDate, nil
	}
	if opts.OnDeploymentOpened != nil {
		opts.OnDeploymentOpened(opened.Deployment.ID)
	}
	fmt.Fprintf(out, "\n%s %s  deploy %s to %s\n", style.Dim("run"),
		style.Bold(opened.Deployment.RunID), project.Result.Definition.Name, opts.Environment)

	// The remote session doubles as the registry push credential; builds
	// and imports push in-process with it, so docker never talks to the
	// managed registry and no credential touches its config or keychain.
	var registryAuth authn.Authenticator
	if target.sessionToken != "" {
		registryAuth = authn.FromConfig(authn.AuthConfig{
			Username: registryUsername, Password: target.sessionToken,
		})
	}
	if err := executeActions(ctx, out, api, opened, project, contexts, registryAuth, opts.Rebuild); err != nil {
		fmt.Fprintf(out, "\n%srun %s %s: %v\n", style.Cross(),
			opened.Deployment.RunID, style.Red("failed"), err)
		fmt.Fprintln(out, "\n"+style.Dim("The environment is unchanged: staged values discarded, target and active revision untouched."))
		return "", errors.New("deployment failed")
	}
	if _, err := api.CompleteDeployment(ctx, opened.Deployment.ID); err != nil {
		return "", err
	}
	if opts.OnDeploymentClosed != nil {
		opts.OnDeploymentClosed()
	}
	if opts.Detach {
		fmt.Fprintf(out, "deployment continues on the server; attach with: skali run attach %s\n", opened.Deployment.RunID)
		return deployOutcomeDetached, nil
	}
	status, err := attachRun(ctx, out, api, opened.Deployment.RunID)
	if err != nil {
		return "", err
	}
	switch status {
	case "succeeded":
		fmt.Fprintln(out, "\n"+style.Check()+style.Bold(style.Green("ready")))
		return deployOutcomeReady, nil
	case "failed":
		return "", fmt.Errorf("run %s failed", opened.Deployment.RunID)
	case "cancelled":
		return "", fmt.Errorf("run %s was cancelled", opened.Deployment.RunID)
	default:
		return deployOutcomeDetached, nil
	}
}

func shortChecksum(checksum string) string {
	checksum = strings.TrimPrefix(checksum, "sha256:")
	if len(checksum) > 12 {
		return checksum[:12]
	}
	return checksum
}
