package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
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
	UseRemoteEnv     bool
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

// selectValues decides the value source per the contract: an explicit
// file, the stored remote values, an auto-discovered file (announced or
// confirmed), or a hard error for guessless non-interactive use.
func selectValues(out io.Writer, project *localProject, opts *deployOptions) (*values.File, error) {
	if opts.UseRemoteEnv {
		return nil, nil
	}
	path := opts.EnvFile
	if path == "" {
		discovered := discoverEnvFile(project.Root, opts.Environment)
		switch {
		case discovered == "":
			if opts.AutoEnvFile {
				return nil, nil
			}
			return nil, errors.New("choose a value source: --env-file PATH or --use-remote-env")
		case opts.AutoEnvFile:
			path = discovered
		case interactive() && !opts.Yes:
			parsed, err := values.ParseFile(discovered)
			if err != nil {
				return nil, err
			}
			plain, secret := countBySecrecy(project.Result, parsed)
			fmt.Fprintf(out, "Upload %s to environment %s?\n", discovered, opts.Environment)
			fmt.Fprintf(out, "  %d plain, %d secret value(s). Values are stored encrypted and never displayed.\n", plain, secret)
			if !confirm(out, "[y/N] ") {
				return nil, nil
			}
			return parsed, nil
		default:
			return nil, errors.New("choose a value source explicitly: --env-file PATH or --use-remote-env")
		}
	}
	parsed, err := values.ParseFile(path)
	if err != nil {
		return nil, err
	}
	return parsed, nil
}

// discoverEnvFile prefers <environment>.env, then .env, in the project root.
func discoverEnvFile(root, environment string) string {
	for _, name := range []string{environment + ".env", ".env"} {
		candidate := filepath.Join(root, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
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
			detail = "DESTRUCTIVE: " + detail
		}
		if action, ok := rebuilt[strings.TrimPrefix(change.Service, "applications.")]; ok && change.Action != "remove" {
			switch action {
			case "build":
				detail = strings.TrimSuffix(detail+"; artifact will be rebuilt", "; ")
			case "import":
				detail = strings.TrimSuffix(detail+"; image will be imported", "; ")
			}
		}
		fmt.Fprintf(out, "  %-7s %-24s %s\n", change.Action, change.Service, detail)
	}
	for _, value := range plan.Values {
		kind := ""
		if value.Secret {
			kind = " (secret)"
		}
		fmt.Fprintf(out, "  %-7s %-24s %s%s\n", "value", value.Name, value.Action, kind)
	}
	if plan.Empty() {
		fmt.Fprintln(out, "  no changes")
	} else if !plan.Destructive() {
		fmt.Fprintln(out, "\nno destructive changes")
	}
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
	fmt.Fprintf(out, "\nThis plan is destructive. Type the environment name to continue: ")
	var answer string
	_, _ = fmt.Scanln(&answer)
	return strings.TrimSpace(answer) == environment
}

// stepLogSink batches engine progress into the journal's client surface.
type stepLogSink struct {
	ctx    context.Context
	api    *client.Client
	stepID string
	buffer []client.LogLine
}

func (s *stepLogSink) Line(level, message string) {
	s.buffer = append(s.buffer, client.LogLine{Level: level, Message: message})
	if len(s.buffer) >= 25 {
		s.Flush()
	}
}

func (s *stepLogSink) Flush() {
	if len(s.buffer) == 0 {
		return
	}
	_ = s.api.AppendStepLogs(s.ctx, s.stepID, s.buffer)
	s.buffer = nil
}

// executeActions performs the client side of the artifact window: builds
// and imports with journaled steps, heartbeats, and server verification.
func executeActions(ctx context.Context, out io.Writer, api *client.Client,
	opened *client.OpenedDeployment, project *localProject, contexts map[string]*build.Context,
	variables map[string]string) error {

	engine := &build.Docker{}
	runID := opened.Deployment.RunID
	for _, action := range opened.Actions {
		switch action.Action {
		case "reuse":
			fmt.Fprintf(out, "artifact for %s is current (%s)\n", action.Application, shortChecksum(action.Digest))
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
			sink := &stepLogSink{ctx: ctx, api: api, stepID: buildStep.ID}
			result, err := engine.Build(ctx, build.BuildRequest{
				ContextDir: contexts[action.Application].Dir,
				Dockerfile: filepath.Join(project.Root, spec.Dockerfile),
				Target:     spec.Target,
				Arguments:  arguments,
				Platform:   action.Platform,
				PushRef:    action.PushRef,
			}, sink)
			sink.Flush()
			stopHeartbeat()
			if err != nil {
				_ = api.SetStepStatus(ctx, buildStep.ID, "failed")
				return failDeployment(ctx, api, opened, fmt.Errorf("build for %s failed: %w", action.Application, err))
			}
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
			sink := &stepLogSink{ctx: ctx, api: api, stepID: importStep.ID}
			result, err := build.Import(ctx, action.Upstream, action.PushRef, false, sink)
			sink.Flush()
			if err != nil {
				_ = api.SetStepStatus(ctx, importStep.ID, "failed")
				return failDeployment(ctx, api, opened, fmt.Errorf("import for %s failed: %w", action.Application, err))
			}
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

	renderer := &clirender.Renderer{
		Out: out,
		TTY: term.IsTerminal(int(os.Stdout.Fd())),
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

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
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
		select {
		case <-attachCtx.Done():
			renderer.Detach()
			fmt.Fprintf(out, "\ndetached from run %s; the deployment continues on the server\n", runID)
			fmt.Fprintf(out, "  reattach  skali run attach %s\n", runID)
			return "detached", nil
		case <-ticker.C:
		}
	}
}

// runDeployFlow is the transcript loop shared by skali plan, deploy, and
// dev. planOnly stops after printing the server-computed plan.
func runDeployFlow(command *cobra.Command, opts *deployOptions, planOnly bool) error {
	ctx := command.Context()
	out := command.OutOrStdout()
	project, err := loadLocalProject(opts.Manifest)
	if err != nil {
		return err
	}
	cfg, contextName, api, err := currentClient()
	if err != nil {
		return err
	}
	master := cfg.Contexts[contextName].Master
	fmt.Fprintf(out, "project      %s (%s)\n", project.Result.Definition.Name, filepath.Base(project.Path))
	fmt.Fprintf(out, "environment  %s (%s)\n", opts.Environment, master)

	projectID, environmentID, err := resolveEnvironmentIDs(ctx, api,
		project.Result.Definition.Name, opts.Environment, opts.CreateMissing)
	if err != nil {
		return err
	}
	definitionVersion, err := api.SubmitDefinition(ctx, projectID, string(project.Source), "yaml")
	if err != nil {
		return err
	}

	// Values: an explicit or discovered local file stages a candidate;
	// otherwise the environment's stored values apply. Local plain values
	// double as the variable space for build-argument resolution.
	file, err := selectValues(out, project, opts)
	if err != nil {
		return err
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
			return fmt.Errorf("%s: %w", file.Path, err)
		}
		maps.Copy(localValues, resolved.Merged())
		if !planOnly {
			staged, err := api.StageValues(ctx, environmentID, file.Values, definitionVersion.DefinitionVersionID)
			if err != nil {
				return err
			}
			candidateID = staged.CandidateID
			fmt.Fprintf(out, "values       %s (%d plain, %d secret)\n", file.Path, len(staged.Plain), len(staged.Secret))
		} else {
			plain, secret := countBySecrecy(project.Result, file)
			fmt.Fprintf(out, "values       %s (%d plain, %d secret; validated, not uploaded)\n", file.Path, plain, secret)
		}
		excludeFiles = append(excludeFiles, file.Path)
	}

	inputs, contexts, err := buildInputs(project, localValues, excludeFiles)
	if err != nil {
		return err
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
		return err
	}
	printPlan(out, planned.Plan, planned.Actions, activeChecksum)
	if planOnly {
		return nil
	}
	if planned.UpToDate {
		fmt.Fprintln(out, "\nnothing to deploy")
		return nil
	}

	// Confirmation: destructive plans demand the typed environment name or
	// the explicit flag; ordinary plans a simple yes.
	if planned.Plan.Destructive() && !opts.AllowDestructive {
		if interactive() && !opts.Yes {
			if !confirmDestructive(out, opts.Environment) {
				return errors.New("aborted")
			}
			request.AllowDestructive = true
		} else {
			return errors.New("plan is destructive; review it and re-run with --allow-destructive")
		}
	} else if planned.Plan.Destructive() {
		request.AllowDestructive = true
	} else if !opts.Yes {
		if !interactive() {
			return errors.New("non-interactive use requires --yes")
		}
		if !confirm(out, "\nContinue? [y/N] ") {
			return errors.New("aborted")
		}
	}

	opened, err := api.OpenDeployment(ctx, environmentID, request)
	if err != nil {
		return err
	}
	if opened.UpToDate {
		fmt.Fprintln(out, "\nnothing to deploy")
		return nil
	}
	fmt.Fprintf(out, "\nrun %s  deploy %s to %s\n", opened.Deployment.RunID,
		project.Result.Definition.Name, opts.Environment)

	if err := executeActions(ctx, out, api, opened, project, contexts, localValues); err != nil {
		fmt.Fprintf(out, "\nrun %s failed: %v\n", opened.Deployment.RunID, err)
		fmt.Fprintln(out, "\nThe environment is unchanged: staged values discarded, target and active revision untouched.")
		return errors.New("deployment failed")
	}
	if _, err := api.CompleteDeployment(ctx, opened.Deployment.ID); err != nil {
		return err
	}
	if opts.Detach {
		fmt.Fprintf(out, "deployment continues on the server; attach with: skali run attach %s\n", opened.Deployment.RunID)
		return nil
	}
	status, err := attachRun(ctx, out, api, opened.Deployment.RunID)
	if err != nil {
		return err
	}
	switch status {
	case "succeeded":
		fmt.Fprintln(out, "\nready")
		return nil
	case "failed":
		return fmt.Errorf("run %s failed", opened.Deployment.RunID)
	case "cancelled":
		return fmt.Errorf("run %s was cancelled", opened.Deployment.RunID)
	default:
		return nil
	}
}

func shortChecksum(checksum string) string {
	checksum = strings.TrimPrefix(checksum, "sha256:")
	if len(checksum) > 12 {
		return checksum[:12]
	}
	return checksum
}
