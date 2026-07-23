package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Hinkolas/skali/internal/build"
	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/manifest"
	"github.com/Hinkolas/skali/internal/values"
)

// deployOptions are the shared knobs of skali plan, deploy, and dev.
type deployOptions struct {
	Environment      string
	Manifest         string
	EnvFile          string
	BuildMode        string
	Yes              bool
	AllowDestructive bool
	Detach           bool
	// AutoEnvFile uses ./.env automatically when present (bare dev).
	AutoEnvFile bool
	// CreateMissing provisions the project and environment through the API
	// when absent (local dev); remote deploys demand they exist.
	CreateMissing bool
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

func interactive() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// resolveEnvironmentIDs finds (or with CreateMissing provisions) the
// project and environment on the target installation.
func resolveEnvironmentIDs(ctx context.Context, api *client.Client, projectName, environmentName string, createMissing bool) (projectID, environmentID string, err error) {
	projects, err := api.ListProjects(ctx)
	if err != nil {
		return "", "", err
	}
	for _, project := range projects {
		if project.Name == projectName {
			projectID = project.ID
		}
	}
	if projectID == "" {
		if !createMissing {
			return "", "", fmt.Errorf("project %s does not exist on this installation", projectName)
		}
		created, err := api.CreateProject(ctx, projectName)
		if err != nil {
			return "", "", err
		}
		projectID = created.ID
	}
	environments, err := api.ListEnvironments(ctx, projectID)
	if err != nil {
		return "", "", err
	}
	for _, environment := range environments {
		if environment.Name == environmentName {
			environmentID = environment.ID
		}
	}
	if environmentID == "" {
		if !createMissing {
			return "", "", fmt.Errorf("environment %s does not exist in project %s", environmentName, projectName)
		}
		created, err := api.CreateEnvironment(ctx, projectID, environmentName)
		if err != nil {
			return "", "", err
		}
		environmentID = created.ID
	}
	return projectID, environmentID, nil
}

// selectValues decides the value source: an explicit --env-file, the bare-dev
// automatic ./.env, an interactively selected override from the project
// root's env files, or nil for the environment's stored values (the default).
func selectValues(out io.Writer, project *localProject, opts *deployOptions) (*values.File, error) {
	path := opts.EnvFile
	if path == "" {
		switch {
		case opts.AutoEnvFile:
			candidate := filepath.Join(project.Root, ".env")
			info, err := os.Stat(candidate)
			if err != nil || info.IsDir() {
				return nil, nil
			}
			path = candidate
		case interactive() && !opts.Yes:
			selected, err := chooseEnvFile(out, bufio.NewReader(os.Stdin), project.Root, opts.Environment)
			if err != nil {
				return nil, err
			}
			if selected == "" {
				return nil, nil
			}
			path = selected
		default:
			return nil, nil
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
	fmt.Fprintf(out, "\nOverride the stored values of environment %s with a local env file?\n", environment)
	options := make([]string, 0, len(files)+1)
	options = append(options, "no, use the stored values")
	for _, file := range files {
		options = append(options, filepath.Base(file))
	}
	choice, err := promptSelect(out, in, options, 0, 0)
	if err != nil {
		return "", err
	}
	if choice == 0 {
		return "", nil
	}
	return files[choice-1], nil
}

// chooseEnvironment lists the project's environments on the target
// installation and asks for one; a single environment selects itself.
func chooseEnvironment(ctx context.Context, out io.Writer, in *bufio.Reader, api *client.Client, projectName string) (string, error) {
	projects, err := api.ListProjects(ctx)
	if err != nil {
		return "", err
	}
	projectID := ""
	for _, project := range projects {
		if project.Name == projectName {
			projectID = project.ID
		}
	}
	if projectID == "" {
		return "", fmt.Errorf("project %s does not exist on this installation", projectName)
	}
	environments, err := api.ListEnvironments(ctx, projectID)
	if err != nil {
		return "", err
	}
	if len(environments) == 0 {
		return "", fmt.Errorf("project %s has no environments on this installation", projectName)
	}
	if len(environments) == 1 {
		return environments[0].Name, nil
	}
	names := make([]string, 0, len(environments))
	for _, environment := range environments {
		names = append(names, environment.Name)
	}
	fmt.Fprintln(out, "Environment:")
	choice, err := promptSelect(out, in, names, 1, -1)
	if err != nil {
		return "", err
	}
	return names[choice], nil
}

// promptSelect renders numbered options starting at start and reads a
// selection, re-asking on invalid input; defaultIndex (relative to options,
// -1 for none) applies on empty input. Returns the relative index.
func promptSelect(out io.Writer, in *bufio.Reader, options []string, start, defaultIndex int) (int, error) {
	for i, option := range options {
		fmt.Fprintf(out, "  %d) %s\n", start+i, option)
	}
	prompt := fmt.Sprintf("Select [%d-%d]", start, start+len(options)-1)
	if defaultIndex >= 0 {
		prompt += fmt.Sprintf(" (%d)", start+defaultIndex)
	}
	prompt += ": "
	for {
		fmt.Fprint(out, prompt)
		line, err := in.ReadString('\n')
		answer := strings.TrimSpace(line)
		if answer == "" && defaultIndex >= 0 {
			return defaultIndex, nil
		}
		if number, convErr := strconv.Atoi(answer); convErr == nil &&
			number >= start && number < start+len(options) {
			return number - start, nil
		}
		if err != nil {
			return 0, errors.New("no valid selection")
		}
	}
}

func countBySecrecy(result *compiler.Result, file *values.File) (plain, secret int) {
	secrecy := make(map[string]bool, len(result.Definition.RequiredVariables))
	for _, requirement := range result.Definition.RequiredVariables {
		secrecy[requirement.Name] = requirement.Secret
	}
	for name, value := range file.Values {
		if value == "" {
			continue
		}
		if secrecy[name] {
			secret++
		} else {
			plain++
		}
	}
	return plain, secret
}

// buildInputs computes the per-application hashes: the dedup key the
// server decides reuse with. Selected env files never enter the context.
func buildInputs(project *localProject, variables map[string]string, excludeFiles []string) (map[string]client.BuildInput, map[string]*build.Context, error) {
	inputs := make(map[string]client.BuildInput)
	contexts := make(map[string]*build.Context)
	platform := "linux/" + runtime.GOARCH
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
		arguments, err := resolveBuildArguments(key, spec.Arguments, variables)
		if err != nil {
			return nil, nil, err
		}
		configHash := build.ConfigHash(dockerfile, spec.Target, arguments)
		inputs[key] = client.BuildInput{
			InputHash:  build.InputHash(collected.TreeHash, configHash, platform),
			ConfigHash: configHash,
			Platform:   platform,
		}
		contexts[key] = collected
	}
	return inputs, contexts, nil
}

func resolveBuildArguments(application string, arguments map[string]compiler.Expression, variables map[string]string) (map[string]string, error) {
	resolved := make(map[string]string, len(arguments))
	for name, expression := range arguments {
		value, err := compiler.ResolveExpression(expression, variables)
		if err != nil {
			return nil, fmt.Errorf("resolve build argument %s of %s: %w "+
				"(build arguments referencing project values need the values available locally; "+
				"pass --env-file)", name, application, err)
		}
		resolved[name] = value
	}
	return resolved, nil
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
		detail := change.Detail
		if change.Destructive {
			detail = style.BoldRed("DESTRUCTIVE:") + " " + detail
		}
		if action, ok := rebuilt[strings.TrimPrefix(change.Service, "applications.")]; ok && change.Action != "remove" {
			switch action {
			case "build":
				detail = strings.TrimSuffix(detail+"; artifact will be rebuilt", "; ")
			case "import":
				detail = strings.TrimSuffix(detail+"; image will be imported", "; ")
			}
		}
		fmt.Fprintf(out, "  %s %-24s %s\n", actionColor(style, change.Action), change.Service, detail)
	}
	for _, value := range plan.Values {
		kind := ""
		if value.Secret {
			kind = " " + style.Dim("(secret)")
		}
		fmt.Fprintf(out, "  %s %-24s %s%s\n", actionColor(style, "value"), value.Name, value.Action, kind)
	}
	if plan.Empty() {
		fmt.Fprintln(out, "  "+style.Dim("no changes"))
	} else if !plan.Destructive() {
		fmt.Fprintln(out, "\n"+style.Dim("no destructive changes"))
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

func confirm(out io.Writer, prompt string) bool {
	fmt.Fprint(out, prompt)
	var answer string
	_, _ = fmt.Scanln(&answer)
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes"
}

// confirmDestructive requires the environment name typed back.
func confirmDestructive(out io.Writer, environment string) bool {
	style := clirender.StyleFor(out)
	fmt.Fprintf(out, "\n%s Type the environment name to continue: ",
		style.BoldRed("This plan is destructive."))
	var answer string
	_, _ = fmt.Scanln(&answer)
	return strings.TrimSpace(answer) == environment
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

// executeActions performs the client side of the artifact window: builds
// and imports with journaled steps, heartbeats, and server verification.
func executeActions(ctx context.Context, out io.Writer, api *client.Client,
	opened *client.OpenedDeployment, project *localProject, contexts map[string]*build.Context,
	variables map[string]string) error {

	engine := &build.Docker{}
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
			arguments, err := resolveBuildArguments(action.Application, spec.Arguments, variables)
			if err != nil {
				return failDeployment(ctx, api, opened, err)
			}

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
				Arguments:  arguments,
				Platform:   action.Platform,
				PushRef:    action.PushRef,
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
			result, err := build.Import(ctx, action.Upstream, action.PushRef, false, &teeSink{inner: sink, task: task})
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
// with an interrupt (detaching never cancels).
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

	for {
		tree, err := api.GetRun(ctx, runID)
		if err != nil {
			return "", err
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
				fmt.Fprintf(out, "\ndetached from run %s; the deployment continues on the server\n", runID)
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

func runDeployFlow(command *cobra.Command, opts *deployOptions, planOnly bool) (string, error) {
	ctx := command.Context()
	out := command.OutOrStdout()
	project, err := loadLocalProject(opts.Manifest)
	if err != nil {
		return "", err
	}
	cfg, remoteName, api, err := currentClient()
	if err != nil {
		return "", err
	}
	style := clirender.StyleFor(out)
	master := cfg.Remotes[remoteName].Master
	fmt.Fprintf(out, "%s      %s %s\n", style.Dim("project"),
		project.Result.Definition.Name, style.Dim("("+filepath.Base(project.Path)+")"))
	if opts.Environment == "" {
		if !interactive() {
			return "", errors.New("--environment is required")
		}
		environment, err := chooseEnvironment(ctx, out, bufio.NewReader(os.Stdin), api,
			project.Result.Definition.Name)
		if err != nil {
			return "", err
		}
		opts.Environment = environment
	}
	fmt.Fprintf(out, "%s  %s %s\n", style.Dim("environment"),
		opts.Environment, style.Dim("("+master+")"))

	projectID, environmentID, err := resolveEnvironmentIDs(ctx, api,
		project.Result.Definition.Name, opts.Environment, opts.CreateMissing)
	if err != nil {
		return "", err
	}
	definitionVersion, err := api.SubmitDefinition(ctx, projectID, string(project.Source), "yaml")
	if err != nil {
		return "", err
	}

	// Values: an explicit or discovered local file stages a candidate;
	// otherwise the environment's stored values apply. Local plain values
	// double as the variable space for build-argument resolution.
	file, err := selectValues(out, project, opts)
	if err != nil {
		return "", err
	}
	localValues := map[string]string{}
	if entries, err := api.EnvironmentValues(ctx, environmentID); err == nil {
		for _, entry := range entries {
			if !entry.Secret {
				localValues[entry.Name] = entry.Value
			}
		}
	}
	candidateID := ""
	var excludeFiles []string
	if file != nil {
		resolved, err := values.Resolve(project.Result.Definition.RequiredVariables, file, values.Options{})
		if err != nil {
			return "", fmt.Errorf("%s: %w", file.Path, err)
		}
		maps.Copy(localValues, resolved.Merged())
		if !planOnly {
			staged, err := api.StageValues(ctx, environmentID, file.Values, definitionVersion.DefinitionVersionID)
			if err != nil {
				return "", err
			}
			candidateID = staged.CandidateID
			fmt.Fprintf(out, "%s       %s %s\n", style.Dim("values"), file.Path,
				style.Dim(fmt.Sprintf("(%d plain, %d secret)", len(staged.Plain), len(staged.Secret))))
		} else {
			plain, secret := countBySecrecy(project.Result, file)
			fmt.Fprintf(out, "%s       %s %s\n", style.Dim("values"), file.Path,
				style.Dim(fmt.Sprintf("(%d plain, %d secret; validated, not uploaded)", plain, secret)))
		}
		excludeFiles = append(excludeFiles, file.Path)
	}

	inputs, contexts, err := buildInputs(project, localValues, excludeFiles)
	if err != nil {
		return "", err
	}
	request := client.DeployRequest{
		DefinitionVersionID: definitionVersion.DefinitionVersionID,
		CandidateID:         candidateID,
		BuildExecutor:       "local",
		Builds:              inputs,
	}

	activeChecksum := ""
	if status, err := api.EnvironmentStatus(ctx, environmentID); err == nil && status.ActiveRevision != nil {
		activeChecksum = status.ActiveRevision.Checksum
	}
	planned, err := api.Plan(ctx, environmentID, request)
	if err != nil {
		return "", err
	}
	printPlan(out, planned.Plan, planned.Actions, activeChecksum)
	if planOnly {
		return deployOutcomePlanned, nil
	}
	if planned.UpToDate {
		fmt.Fprintln(out, "\nnothing to deploy")
		return deployOutcomeUpToDate, nil
	}

	// Confirmation: destructive plans demand the typed environment name or
	// the explicit flag; ordinary plans a simple yes.
	if planned.Plan.Destructive() && !opts.AllowDestructive {
		if interactive() && !opts.Yes {
			if !confirmDestructive(out, opts.Environment) {
				return "", errors.New("aborted")
			}
			request.AllowDestructive = true
		} else {
			return "", errors.New("plan is destructive; review it and re-run with --allow-destructive")
		}
	} else if planned.Plan.Destructive() {
		request.AllowDestructive = true
	} else if !opts.Yes {
		if !interactive() {
			return "", errors.New("non-interactive use requires --yes")
		}
		if !confirm(out, "\nContinue? [y/N] ") {
			return "", errors.New("aborted")
		}
	}

	opened, err := api.OpenDeployment(ctx, environmentID, request)
	if err != nil {
		return "", err
	}
	if opened.UpToDate {
		fmt.Fprintln(out, "\nnothing to deploy")
		return deployOutcomeUpToDate, nil
	}
	fmt.Fprintf(out, "\n%s %s  deploy %s to %s\n", style.Dim("run"),
		style.Bold(opened.Deployment.RunID), project.Result.Definition.Name, opts.Environment)

	if err := executeActions(ctx, out, api, opened, project, contexts, localValues); err != nil {
		fmt.Fprintf(out, "\n%srun %s %s: %v\n", style.Cross(),
			opened.Deployment.RunID, style.Red("failed"), err)
		fmt.Fprintln(out, "\n"+style.Dim("The environment is unchanged: staged values discarded, target and active revision untouched."))
		return "", errors.New("deployment failed")
	}
	if _, err := api.CompleteDeployment(ctx, opened.Deployment.ID); err != nil {
		return "", err
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
