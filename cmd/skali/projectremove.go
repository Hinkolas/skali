package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
)

// projectRemovePollInterval paces the wait for a project's environments to
// go; a removal takes as long as its slowest environment's purge.
const projectRemovePollInterval = 2 * time.Second

// newProjectCommand groups the commands about a project itself. Its
// environments are skali env, its members skali access.
func newProjectCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "project",
		Short: "Manage projects",
	}
	command.AddCommand(newProjectRmCommand())
	return command
}

func newProjectRmCommand() *cobra.Command {
	var remote string
	var yes, detach bool
	command := &cobra.Command{
		Use:               "remove [project]",
		Aliases:           []string{"rm"},
		Short:             "Remove a project with every environment and all its data",
		ValidArgsFunction: completeProjects,
		Long: "Removes every environment of the project exactly as skali env remove would,\n" +
			"with its volumes, databases, buckets, values, revisions, and history, and the\n" +
			"project goes with the last of them. This is one-way. Without a name the\n" +
			"checkout's project is removed. The command follows the removal until the\n" +
			"project is gone; --detach returns right away, and the removal continues on\n" +
			"the server either way. Repeating the command resumes a removal that was\n" +
			"interrupted. Project admin and a recent login are required.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			ctx := command.Context()
			out := command.OutOrStdout()
			in := bufio.NewReader(command.InOrStdin())
			style := clirender.StyleFor(out)
			start, err := os.Getwd()
			if err != nil {
				return err
			}
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			scope, err := resolveQueryProject(ctx, start, name, "", remote)
			if err != nil {
				return err
			}
			names := make([]string, 0, len(scope.environments))
			for _, environment := range scope.environments {
				names = append(names, environment.Name)
			}
			environments := "none"
			if len(names) > 0 {
				environments = strings.Join(names, ", ")
			}
			printHeader(out, style,
				headerRow{"remote", scope.remoteName, scope.api.Master()},
				headerRow{"project", scope.project.Name, ""},
				headerRow{"environments", environments, ""})
			if !yes {
				if !cliprompt.Interactive() {
					return errors.New("non-interactive use requires --yes")
				}
				description := "Removes the project with its values, revisions, and history. This is one-way."
				if len(names) > 0 {
					description = fmt.Sprintf("Removes %s with their volumes, databases, and buckets, then "+
						"the project with its values, revisions, and history. This is one-way.",
						countEnvironments(len(names)))
				}
				confirmed, err := promptSession(out, in).ConfirmTyped(ctx,
					fmt.Sprintf("Remove project %s on %s?", scope.project.Name, scope.remoteName),
					description, scope.project.Name)
				if err != nil {
					return err
				}
				if !confirmed {
					return errors.New("aborted")
				}
			}
			var deletion *client.ProjectDeletion
			err = withReauth(ctx, out, in, scope.api, func() (err error) {
				deletion, err = scope.api.DeleteProject(ctx, scope.project.ID)
				return err
			})
			if err != nil {
				return err
			}
			if deletion.Deleted {
				fmt.Fprintf(out, "\n%sproject %s is removed\n", style.Check(), scope.project.Name)
				return nil
			}
			fmt.Fprintf(out, "\nremoving %s; the project goes with the last of them\n",
				countEnvironments(len(deletion.Purges)))
			if detach {
				fmt.Fprintln(out, style.Dim("follow it with skali env list --project "+scope.project.Name+
					"; skali project remove "+scope.project.Name+" resumes an interrupted removal"))
				return nil
			}
			return waitProjectRemoved(ctx, out, scope)
		},
	}
	command.Flags().StringVar(&remote, "remote", "", remoteFlagHelp)
	command.Flags().BoolVar(&yes, "yes", false, "skip the typed confirmation")
	command.Flags().BoolVar(&detach, "detach", false, "start the removal and return without following it")
	return command
}

func countEnvironments(n int) string {
	if n == 1 {
		return "1 environment"
	}
	return fmt.Sprintf("%d environments", n)
}

// waitProjectRemoved reports each environment as it goes and returns once
// the project itself is gone. Ctrl-C ends the wait, never the removal.
func waitProjectRemoved(ctx context.Context, out io.Writer, scope *queryProject) error {
	style := clirender.StyleFor(out)
	waitCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	fmt.Fprintln(out, style.Dim("Ctrl-C stops waiting; the removal continues on the server"))

	// stopped ends the wait without ending the removal; the parent
	// context ending is an error, the user's Ctrl-C is not.
	stopped := func() error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		fmt.Fprintf(out, "\nstopped waiting; the removal of %s continues on the server\n", scope.project.Name)
		fmt.Fprintf(out, "  %s  skali env list --project %s\n", style.Dim("progress"), scope.project.Name)
		return nil
	}
	remaining := make([]string, 0, len(scope.environments))
	for _, environment := range scope.environments {
		remaining = append(remaining, environment.Name)
	}
	for {
		_, err := scope.api.GetProject(waitCtx, scope.project.ID)
		if isNotFound(err) {
			for _, name := range remaining {
				fmt.Fprintf(out, "%s%s removed\n", style.Check(), name)
			}
			fmt.Fprintf(out, "\n%sproject %s is removed; nothing of it remains\n", style.Check(), scope.project.Name)
			return nil
		}
		var environments []client.Environment
		if err == nil {
			environments, err = scope.api.ListEnvironments(waitCtx, scope.project.ID)
		}
		if err != nil {
			if waitCtx.Err() != nil {
				return stopped()
			}
			return err
		}
		remaining = slices.DeleteFunc(remaining, func(name string) bool {
			if findEnvironment(environments, name) != nil {
				return false
			}
			fmt.Fprintf(out, "%s%s removed\n", style.Check(), name)
			return true
		})
		select {
		case <-waitCtx.Done():
			return stopped()
		case <-time.After(projectRemovePollInterval):
		}
	}
}
