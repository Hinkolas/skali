package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/checkout"
	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/manifest"
	"github.com/Hinkolas/skali/internal/values"
)

// promoteContext is the resolved source of one deploy --from invocation.
type promoteContext struct {
	remoteName  string
	master      string
	api         *client.Client
	binding     *checkout.Target
	projectID   string
	projectName string
	source      client.Environment
}

// resolvePromoteContext resolves the remote, project, and source environment
// of a promotion without reading any manifest: an explicit --remote wins
// (the checkout binding is ignored), otherwise the binding names the
// project when one is discovered next to a manifest, and without either
// the source environment's name is resolved by scanning the current
// remote's projects.
func resolvePromoteContext(ctx context.Context, from, override string) (*promoteContext, error) {
	cfg, err := cliconfig.Load()
	if err != nil {
		return nil, err
	}
	start, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	var binding *checkout.Target
	if override == "" {
		if path, err := manifest.Discover("", start); err == nil {
			if binding, err = checkout.Load(filepath.Dir(path)); err != nil {
				return nil, err
			}
		}
	}
	var remoteName string
	var remote *cliconfig.Remote
	switch {
	case override != "":
		remoteName = override
		if remote, err = remoteByName(cfg, remoteName); err != nil {
			return nil, err
		}
	case binding != nil:
		name, found, ok := lookupRemoteByMaster(cfg, binding.Master)
		if !ok {
			return nil, fmt.Errorf("no remote for %s on this machine; run skali remote add %s",
				binding.Master, binding.Master)
		}
		remoteName, remote = name, found
	default:
		if remoteName, remote, err = cfg.Current(); err != nil {
			return nil, err
		}
	}
	api := remoteClient(cfg, remote)

	if binding != nil {
		project, err := findProject(ctx, api, binding.Project)
		if err != nil {
			return nil, err
		}
		if project == nil {
			return nil, fmt.Errorf("project %s does not exist on %s", binding.Project, api.Master())
		}
		environments, err := api.ListEnvironments(ctx, project.ID)
		if err != nil {
			return nil, err
		}
		source := findEnvironment(environments, from)
		if source == nil {
			return nil, fmt.Errorf("environment %s does not exist in project %s on %s",
				from, binding.Project, api.Master())
		}
		return &promoteContext{
			remoteName: remoteName, master: remote.Master, api: api, binding: binding,
			projectID: project.ID, projectName: binding.Project, source: *source,
		}, nil
	}

	// Without a binding the source environment names the project: scan the
	// remote for it.
	projects, err := api.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	for _, project := range projects {
		environments, err := api.ListEnvironments(ctx, project.ID)
		if err != nil {
			return nil, err
		}
		if source := findEnvironment(environments, from); source != nil {
			return &promoteContext{
				remoteName: remoteName, master: remote.Master, api: api,
				projectID: project.ID, projectName: project.Name, source: *source,
			}, nil
		}
	}
	return nil, fmt.Errorf("environment %s not found on this installation", from)
}

// runPromoteFlow is the deploy --from transcript: the source environment's
// active revision re-deploys into the target environment with the target's
// own values and data. Nothing compiles or builds locally; every artifact
// action is a reuse and completion follows the open immediately. The
// checkout binding is never rewritten by a promotion.
func runPromoteFlow(command *cobra.Command, opts *deployOptions, planOnly bool) (string, error) {
	ctx := command.Context()
	out := command.OutOrStdout()
	style := clirender.StyleFor(out)
	prompts := cliprompt.Interactive() && !opts.Yes

	promote, err := resolvePromoteContext(ctx, opts.From, opts.Remote)
	if err != nil {
		return "", err
	}
	api := promote.api
	fmt.Fprintf(out, "%s       %s %s\n", style.Dim("remote"), promote.remoteName,
		style.Dim("("+promote.master+")"))
	fmt.Fprintf(out, "%s      %s\n", style.Dim("project"), promote.projectName)

	pointer, err := api.Target(ctx, promote.source.ID)
	if err != nil {
		return "", err
	}
	if pointer.ActiveRevisionID == nil {
		return "", fmt.Errorf("environment %s has no active revision to promote", opts.From)
	}
	revisions, err := api.ListRevisions(ctx, promote.source.ID)
	if err != nil {
		return "", err
	}
	var sourceRevision *client.RevisionSummary
	for index := range revisions {
		if revisions[index].ID == *pointer.ActiveRevisionID {
			sourceRevision = &revisions[index]
			break
		}
	}
	if sourceRevision == nil {
		return "", fmt.Errorf("environment %s has no active revision to promote", opts.From)
	}
	fmt.Fprintf(out, "%s     %s %s\n", style.Dim("revision"), shortChecksum(sourceRevision.Checksum),
		style.Dim("(active in "+opts.From+")"))

	// The bound environment is the target default, exactly like an ordinary
	// deploy; --environment overrides it for one invocation.
	if opts.Environment == "" && promote.binding != nil {
		opts.Environment = promote.binding.Environment
	}
	environmentID, err := resolveEnvironmentTarget(ctx, out, bufio.NewReader(os.Stdin), api,
		promote.projectID, promote.projectName, promote.master, opts, planOnly, prompts)
	if err != nil {
		return "", err
	}
	if environmentID == promote.source.ID {
		return "", errors.New("--from and the target environment name the same environment; pass --environment")
	}

	candidateID := ""
	if opts.EnvFile != "" {
		file, err := values.ParseFile(opts.EnvFile)
		if err != nil {
			return "", err
		}
		if planOnly {
			fmt.Fprintf(out, "%s       %s %s\n", style.Dim("values"), file.Path,
				style.Dim("(applies at deploy; the plan uses stored values)"))
		} else {
			staged, err := api.StageValues(ctx, environmentID, file.Values, sourceRevision.DefinitionVersionID)
			if err != nil {
				return "", err
			}
			candidateID = staged.CandidateID
			fmt.Fprintf(out, "%s       %s %s\n", style.Dim("values"), file.Path,
				style.Dim(fmt.Sprintf("(%d staged)", len(staged.Staged))))
			if len(staged.Skipped) > 0 {
				fmt.Fprintf(out, "  %s\n", style.Yellow("warning: skipped keys not referenced by the manifest: "+
					strings.Join(staged.Skipped, ", ")))
			}
		}
	}

	activeChecksum := ""
	if status, err := api.EnvironmentStatus(ctx, environmentID); err == nil {
		if status.ActiveRevision != nil {
			activeChecksum = status.ActiveRevision.Checksum
		}
	} else {
		fmt.Fprintf(out, "  %s\n", style.Yellow(fmt.Sprintf(
			"warning: could not fetch environment status: %v", err)))
	}
	request := client.DeployRequest{
		FromEnvironmentID: promote.source.ID,
		CandidateID:       candidateID,
		BuildExecutor:     "local",
		Force:             opts.Force,
	}
	planned, err := api.Plan(ctx, environmentID, request)
	if err != nil {
		return "", err
	}
	printPlan(out, planned.Plan, planned.Actions, activeChecksum)
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
	fmt.Fprintf(out, "\n%s %s  promote %s to %s\n", style.Dim("run"),
		style.Bold(opened.Deployment.RunID), opts.From, opts.Environment)

	// Every action is a reuse by construction; anything else is a server
	// bug and fails the window rather than guessing.
	tasks := clirender.NewTasks(out)
	for _, action := range opened.Actions {
		if action.Action != "reuse" {
			_ = api.FailDeployment(ctx, opened.Deployment.ID)
			return "", fmt.Errorf("unexpected %s action for %s in a promotion", action.Action, action.Application)
		}
		tasks.Start("artifact for " + action.Application).
			Skip("current, " + shortChecksum(action.Digest))
	}
	if _, err := api.CompleteDeployment(ctx, opened.Deployment.ID); err != nil {
		return "", err
	}
	if opts.Detach {
		fmt.Fprintf(out, "deployment continues on the server; attach with: %s\n", runAttachHint(opts.Remote, opened.Deployment.RunID))
		return deployOutcomeDetached, nil
	}
	status, err := attachRun(ctx, out, api, opened.Deployment.RunID, opts.Remote)
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
