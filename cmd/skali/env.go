package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
)

// newEnvCommand lists and configures a project's environments: access
// ceiling, deploy policy, priority, creation, and purge. Who may do what on
// an environment is skali access; this group is about the environment
// itself.
func newEnvCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "env",
		Short: "List and configure environments",
		Long: "Environments of the checkout's project (or --project). Listing needs\n" +
			"project read, creating needs project maintain (the creator becomes\n" +
			"admin of the new environment), settings and removal need environment\n" +
			"admin and a recent login.",
	}
	command.AddCommand(newEnvLsCommand(), newEnvCreateCommand(), newEnvSetCommand(), newEnvRmCommand())
	return command
}

func newEnvLsCommand() *cobra.Command {
	var project, remote string
	command := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the project's environments with your access and their settings",
		Args:    cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			scope, err := resolveAccessScope(command.Context(), project, "", remote)
			if err != nil {
				return err
			}
			renderEnvironmentTable(command.OutOrStdout(), scope)
			return nil
		},
	}
	addAccessScopeFlags(command, &project, &remote)
	return command
}

func renderEnvironmentTable(out io.Writer, scope *accessScope) {
	style := clirender.StyleFor(out)
	fmt.Fprintf(out, "%s  %s %s\n\n", style.Dim("project"), scope.project.Name, style.Dim("("+scope.remoteName+")"))
	if len(scope.environments) == 0 {
		fmt.Fprintln(out, style.Dim("no environments; skali env create <name> or skali deploy creates one"))
		return
	}
	bound := ""
	if scope.binding != nil {
		bound = scope.binding.Environment
	}
	rows := make([][]string, 0, len(scope.environments))
	for i := range scope.environments {
		environment := &scope.environments[i]
		name := environment.Name
		if name == bound {
			name += " (bound)"
		}
		if environment.Locked() || environment.Settings == nil {
			rows = append(rows, []string{name, "locked", "-", "-", "-", "-"})
			continue
		}
		created := "-"
		if environment.CreatedAt != nil {
			created = environment.CreatedAt.Local().Format("2006-01-02 15:04")
		}
		rows = append(rows, []string{name, environment.Access, environment.Settings.Priority,
			describeDeployPolicy(environment.Settings), environment.Settings.MaxRole, created})
	}
	renderColumns(out, []string{"NAME", "ACCESS", "PRIORITY", "POLICY", "CEILING", "CREATED"}, rows)
}

func describeDeployPolicy(settings *client.EnvironmentSettings) string {
	if settings.DeployPolicy != "promote-only" {
		return settings.DeployPolicy
	}
	if len(settings.PromoteFrom) == 0 {
		return "promote-only (from any)"
	}
	return "promote-only (from " + strings.Join(settings.PromoteFrom, ", ") + ")"
}

func newEnvCreateCommand() *cobra.Command {
	var project, remote, priority string
	command := &cobra.Command{
		Use:   "create <name>",
		Short: "Create an environment",
		Long: "Creates an environment in the project; you become its admin. Priority high\n" +
			"(instance admins only) marks an environment that keeps running when\n" +
			"resources are tight; it starts with a read ceiling for inheriting members.",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			ctx := command.Context()
			out := command.OutOrStdout()
			style := clirender.StyleFor(out)
			if priority != "" && priority != "normal" && priority != "high" {
				return errors.New("--priority must be normal or high")
			}
			scope, err := resolveAccessScope(ctx, project, "", remote)
			if err != nil {
				return err
			}
			created, err := scope.api.CreateEnvironment(ctx, scope.project.ID, args[0], priority)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "created environment %s in project %s\n", created.Name, scope.project.Name)
			if created.Settings != nil {
				fmt.Fprintf(out, "%s\n", style.Dim(fmt.Sprintf("priority %s, ceiling %s, deploy policy %s",
					created.Settings.Priority, created.Settings.MaxRole, created.Settings.DeployPolicy)))
				if created.Settings.Priority == "high" {
					fmt.Fprintln(out, style.Dim("high priority starts read-only for inheriting members; consider skali env set --deploy-policy promote-only"))
				}
			}
			return nil
		},
	}
	addAccessScopeFlags(command, &project, &remote)
	command.Flags().StringVar(&priority, "priority", "", "normal (default) or high (instance admins only)")
	return command
}

func newEnvSetCommand() *cobra.Command {
	var project, environment, remote string
	var maxRole, deployPolicy, promoteFrom, priority string
	command := &cobra.Command{
		Use:   "set",
		Short: "Change an environment's ceiling, deploy policy, or priority",
		Long: "Changes settings of the bound environment (or --environment); only the\n" +
			"flags given change. --max-role caps the roles members inherit from their\n" +
			"project role (explicit per-environment roles and project admins are not\n" +
			"capped). --deploy-policy promote-only refuses direct deploys; --promote-from\n" +
			"names the environments promotions may come from (any to allow every\n" +
			"environment of the project). Raising --priority to high is instance admin\n" +
			"only.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			ctx := command.Context()
			out := command.OutOrStdout()
			in := bufio.NewReader(command.InOrStdin())
			patch := client.EnvironmentSettingsPatch{}
			if maxRole != "" {
				if !slices.Contains(cellRoles, maxRole) {
					return fmt.Errorf("--max-role must be one of %s", strings.Join(cellRoles, ", "))
				}
				patch.MaxRole = &maxRole
			}
			if deployPolicy != "" {
				if deployPolicy != "direct" && deployPolicy != "promote-only" {
					return errors.New("--deploy-policy must be direct or promote-only")
				}
				patch.DeployPolicy = &deployPolicy
			}
			if command.Flags().Changed("promote-from") {
				sources := []string{}
				if promoteFrom != "any" && promoteFrom != "" {
					for _, name := range strings.Split(promoteFrom, ",") {
						if name = strings.TrimSpace(name); name != "" {
							sources = append(sources, name)
						}
					}
				}
				patch.PromoteFrom = &sources
			}
			if priority != "" {
				if priority != "normal" && priority != "high" {
					return errors.New("--priority must be normal or high")
				}
				patch.Priority = &priority
			}
			if patch.MaxRole == nil && patch.DeployPolicy == nil && patch.PromoteFrom == nil && patch.Priority == nil {
				return errors.New("nothing to change: give --max-role, --deploy-policy, --promote-from, and/or --priority")
			}
			scope, err := resolveEnvScope(ctx, project, environment, remote)
			if err != nil {
				return err
			}
			var updated *client.Environment
			err = withReauth(ctx, out, in, scope.api, func() (err error) {
				updated, err = scope.api.UpdateEnvironmentSettings(ctx, scope.environment.ID, patch)
				return err
			})
			if err != nil {
				return err
			}
			printEnvironmentSettings(out, scope, updated)
			if patch.Priority != nil && scope.environment.Settings != nil &&
				updated.Settings != nil && scope.environment.Settings.Priority != updated.Settings.Priority {
				// Priority renders live: the kernel moves the application
				// workloads onto the class now, which rolls their pods.
				fmt.Fprintln(out, clirender.StyleFor(out).Dim(
					"application pods roll onto priority class "+priorityClassName(updated.Settings.Priority)))
			}
			return nil
		},
	}
	addAccessScopeFlags(command, &project, &remote)
	command.Flags().StringVar(&environment, "environment", "", "environment to change; defaults to the checkout binding")
	command.Flags().StringVar(&maxRole, "max-role", "", "ceiling for inherited roles: none, read, deploy, maintain, or admin")
	command.Flags().StringVar(&deployPolicy, "deploy-policy", "", "direct or promote-only")
	command.Flags().StringVar(&promoteFrom, "promote-from", "", "comma-separated source environments for promotions, or any")
	command.Flags().StringVar(&priority, "priority", "", "normal or high")
	return command
}

// resolveEnvScope resolves the environment an env command changes: the
// --environment flag, else the checkout binding's environment.
func resolveEnvScope(ctx context.Context, project, environment, remote string) (*accessScope, error) {
	scope, err := resolveAccessScope(ctx, project, environment, remote)
	if err != nil {
		return nil, err
	}
	if scope.environment == nil {
		if scope.binding == nil || scope.binding.Environment == "" {
			return nil, errEnvironmentRequired
		}
		scope.environment = findEnvironment(scope.environments, scope.binding.Environment)
		if scope.environment == nil {
			return nil, fmt.Errorf("environment %s does not exist in project %s on %s",
				scope.binding.Environment, scope.project.Name, scope.api.Master())
		}
	}
	return scope, nil
}

// priorityClassName mirrors the server's mapping of an environment priority
// to the PriorityClass its application pods carry (internal/layout); the
// CLI names it in its note without importing the cluster packages.
func priorityClassName(priority string) string {
	if priority == "high" {
		return "skali-high"
	}
	return "skali-normal"
}

func printEnvironmentSettings(out io.Writer, scope *accessScope, environment *client.Environment) {
	style := clirender.StyleFor(out)
	fmt.Fprintf(out, "environment    %s %s\n", environment.Name,
		style.Dim("("+scope.project.Name+" on "+scope.remoteName+")"))
	if environment.Settings == nil {
		return
	}
	fmt.Fprintf(out, "max role       %s\n", environment.Settings.MaxRole)
	fmt.Fprintf(out, "deploy policy  %s\n", describeDeployPolicy(environment.Settings))
	fmt.Fprintf(out, "priority       %s\n", environment.Settings.Priority)
}

func newEnvRmCommand() *cobra.Command {
	var project, remote string
	var yes bool
	command := &cobra.Command{
		Use:     "remove <name>",
		Aliases: []string{"rm"},
		Short:   "Purge an environment: its workloads, volumes, values, revisions, and history",
		Long: "Tears the environment down with purge: the namespace with its volumes goes,\n" +
			"and the environment is deleted with all values, revisions, and history. This\n" +
			"is one-way. Environment admin and a recent login are required.",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			ctx := command.Context()
			out := command.OutOrStdout()
			in := bufio.NewReader(command.InOrStdin())
			style := clirender.StyleFor(out)
			scope, err := resolveAccessScope(ctx, project, args[0], remote)
			if err != nil {
				return err
			}
			if !yes {
				fmt.Fprintln(out, style.BoldRed(fmt.Sprintf("This destroys environment %s of project %s on %s completely:",
					scope.environment.Name, scope.project.Name, scope.remoteName)))
				fmt.Fprintln(out, "  its namespace including all volumes, and its values,")
				fmt.Fprintln(out, "  revisions, and history. This is one-way.")
				confirmed, err := promptSession(out, in).Confirm(ctx, cliprompt.ConfirmOptions{
					Title: fmt.Sprintf("Purge environment %s?", scope.environment.Name),
				})
				if err != nil {
					return confirmError(err)
				}
				if !confirmed {
					return errors.New("aborted")
				}
			}
			var runID string
			err = withReauth(ctx, out, in, scope.api, func() (err error) {
				runID, err = scope.api.TeardownEnvironment(ctx, scope.environment.ID, true)
				return err
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "purge started  %s\n", style.Bold(runID))
			fmt.Fprintln(out, style.Dim("follow it with "+runAttachHint(remote, runID)))
			return nil
		},
	}
	addAccessScopeFlags(command, &project, &remote)
	command.Flags().BoolVar(&yes, "yes", false, "skip the confirmation")
	return command
}
