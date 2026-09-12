package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
)

// Roles on the one ladder both levels share; the server is the authority,
// these lists only make the CLI refuse obvious typos before a round trip.
var (
	projectRoles = []string{"read", "deploy", "maintain", "admin"}
	cellRoles    = []string{"none", "read", "deploy", "maintain", "admin"}
)

const remoteFlagHelp = "remote to target for this one invocation, ignoring the checkout binding and the current remote"

// newAccessCommand groups who-may-do-what on a project: memberships (a
// user's role on the whole project) and cells (an explicit role on one
// environment). The same verbs serve both; --environment picks the level.
func newAccessCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "access",
		Short: "Manage who may do what on a project",
		Long: "Project access is one role ladder, none < read < deploy < maintain < admin,\n" +
			"used at two levels: a member's project role applies to every environment\n" +
			"unless an explicit per-environment role (set with --environment) overrides\n" +
			"it, and an environment's ceiling (skali env set --max-role) caps inherited\n" +
			"roles. Listing needs project read; changes need project admin (environment\n" +
			"admin for --environment) and a recent login.\n\n" +
			"The project is the checkout's (its binding or manifest) unless --project\n" +
			"names one.",
	}
	command.AddCommand(newAccessLsCommand(), newAccessSetCommand(), newAccessRmCommand())
	return command
}

// accessScope is the resolved project of an access command, with the
// environment when one was named.
type accessScope struct {
	*queryProject
	environment *client.Environment
}

func resolveAccessScope(ctx context.Context, project, environment, remote string) (*accessScope, error) {
	start, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	scope, err := resolveQueryProject(ctx, start, project, "", remote)
	if err != nil {
		return nil, err
	}
	resolved := &accessScope{queryProject: scope}
	if environment != "" {
		resolved.environment = findEnvironment(scope.environments, environment)
		if resolved.environment == nil {
			return nil, fmt.Errorf("environment %s does not exist in project %s on %s",
				environment, scope.project.Name, scope.api.Master())
		}
	}
	return resolved, nil
}

func addAccessScopeFlags(command *cobra.Command, project, remote *string) {
	command.Flags().StringVar(project, "project", "", "project to manage; defaults to the checkout's project")
	command.Flags().StringVar(remote, "remote", "", remoteFlagHelp)
}

func newAccessLsCommand() *cobra.Command {
	var project, remote string
	command := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "Show members and their effective role on every environment",
		Args:    cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			ctx := command.Context()
			scope, err := resolveAccessScope(ctx, project, "", remote)
			if err != nil {
				return err
			}
			members, err := scope.api.ListMembers(ctx, scope.project.ID)
			if err != nil {
				return err
			}
			renderAccessGrid(command.OutOrStdout(), scope, members)
			return nil
		},
	}
	addAccessScopeFlags(command, &project, &remote)
	return command
}

// renderAccessGrid prints members as rows and environments as columns. A
// cell shows the effective role the server computed, marked with * when an
// explicit per-environment role produces it; environments the caller may
// not read show "-" and carry a (locked) header.
func renderAccessGrid(out io.Writer, scope *accessScope, members []client.Member) {
	style := clirender.StyleFor(out)
	fmt.Fprintf(out, "%s  %s %s  %s\n\n", style.Dim("project"), scope.project.Name,
		style.Dim("("+scope.remoteName+")"), style.Dim("your role: "+scope.project.Access.Role))
	if len(members) == 0 {
		fmt.Fprintln(out, style.Dim("no members; instance admins see every project without membership"))
		return
	}
	headers := []string{"MEMBER", "PROJECT"}
	for _, environment := range scope.environments {
		header := environment.Name
		if environment.Locked() {
			header += " (locked)"
		}
		headers = append(headers, header)
	}
	rows := make([][]string, 0, len(members))
	explicit := false
	for i := range members {
		member := &members[i]
		projectRole := member.Role
		if member.InstanceAdmin {
			projectRole += " (instance admin)"
		}
		row := []string{member.Email, projectRole}
		for _, environment := range scope.environments {
			entry, ok := member.Environments[environment.Name]
			if !ok {
				row = append(row, "-")
				continue
			}
			cell := entry.Role
			if entry.Cell != nil {
				cell += "*"
				explicit = true
			}
			row = append(row, cell)
		}
		rows = append(rows, row)
	}
	renderColumns(out, headers, rows)
	if explicit {
		fmt.Fprintln(out, style.Dim("* explicit per-environment role"))
	}
}

// renderColumns prints a header row and rows with every column as wide as
// its widest value, two spaces apart, the last column unpadded.
func renderColumns(out io.Writer, headers []string, rows [][]string) {
	widths := make([]int, len(headers))
	for i, header := range headers {
		widths[i] = len(header)
	}
	for _, row := range rows {
		for i, cell := range row {
			widths[i] = max(widths[i], len(cell))
		}
	}
	line := func(cells []string) {
		var b strings.Builder
		for i, cell := range cells {
			if i == len(cells)-1 {
				b.WriteString(cell)
				continue
			}
			b.WriteString(fmt.Sprintf("%-*s  ", widths[i], cell))
		}
		fmt.Fprintln(out, strings.TrimRight(b.String(), " "))
	}
	line(headers)
	for _, row := range rows {
		line(row)
	}
}

func newAccessSetCommand() *cobra.Command {
	var project, environment, remote string
	command := &cobra.Command{
		Use:   "set <user> <role>",
		Short: "Set a member's project role, or their explicit role on one environment",
		Long: "Without --environment sets the user's project role (read, deploy, maintain,\n" +
			"admin), adding them as a member when needed. With --environment sets an\n" +
			"explicit role on that environment only (none locks it for them); the user\n" +
			"must already be a member. <user> is an email address or a user id.",
		Args: cobra.ExactArgs(2),
		RunE: func(command *cobra.Command, args []string) error {
			ctx := command.Context()
			out := command.OutOrStdout()
			in := bufio.NewReader(command.InOrStdin())
			user, role := args[0], args[1]
			if environment == "" {
				if role == "none" {
					return errors.New("none is not a project role: lock one environment with --environment, or remove the membership with skali access remove")
				}
				if !slices.Contains(projectRoles, role) {
					return fmt.Errorf("role must be one of %s", strings.Join(projectRoles, ", "))
				}
			} else if !slices.Contains(cellRoles, role) {
				return fmt.Errorf("role must be one of %s", strings.Join(cellRoles, ", "))
			}
			scope, err := resolveAccessScope(ctx, project, environment, remote)
			if err != nil {
				return err
			}
			if scope.environment == nil {
				var member *client.Member
				err := withReauth(ctx, out, in, scope.api, func() (err error) {
					member, err = scope.api.SetMember(ctx, scope.project.ID, user, role)
					return err
				})
				if err != nil {
					return err
				}
				fmt.Fprintf(out, "%s  %s on project %s\n", member.Email, member.Role, scope.project.Name)
				return nil
			}
			var cell *client.Member
			err = withReauth(ctx, out, in, scope.api, func() (err error) {
				cell, err = scope.api.SetEnvironmentAccess(ctx, scope.environment.ID, user, role)
				return err
			})
			if err != nil {
				var apiErr *client.APIError
				if errors.As(err, &apiErr) && apiErr.Status == 409 {
					return fmt.Errorf("%w; grant a project role first: skali access set %s read", err, user)
				}
				return err
			}
			fmt.Fprintf(out, "%s  %s on environment %s\n", cell.Email, cell.Role, scope.environment.Name)
			return nil
		},
	}
	addAccessScopeFlags(command, &project, &remote)
	command.Flags().StringVar(&environment, "environment", "", "set the role on this environment only")
	return command
}

func newAccessRmCommand() *cobra.Command {
	var project, environment, remote string
	var yes bool
	command := &cobra.Command{
		Use:     "remove <user>",
		Aliases: []string{"rm"},
		Short:   "Remove a member, or drop their explicit role on one environment",
		Long: "Without --environment removes the membership; the user's explicit\n" +
			"per-environment roles go with it and the project disappears for them.\n" +
			"With --environment drops only the explicit role there, so their project\n" +
			"role applies again.",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			ctx := command.Context()
			out := command.OutOrStdout()
			in := bufio.NewReader(command.InOrStdin())
			user := args[0]
			scope, err := resolveAccessScope(ctx, project, environment, remote)
			if err != nil {
				return err
			}
			if scope.environment != nil {
				err := withReauth(ctx, out, in, scope.api, func() error {
					return scope.api.RemoveEnvironmentAccess(ctx, scope.environment.ID, user)
				})
				if err != nil {
					return err
				}
				fmt.Fprintf(out, "removed the explicit role of %s on environment %s; their project role applies\n",
					user, scope.environment.Name)
				return nil
			}
			if !yes {
				confirmed, err := promptSession(out, in).Confirm(ctx, cliprompt.ConfirmOptions{
					Title: fmt.Sprintf("Remove %s from project %s? Their per-environment roles go with it.",
						user, scope.project.Name),
				})
				if err != nil {
					return confirmError(err)
				}
				if !confirmed {
					return errors.New("aborted")
				}
			}
			err = withReauth(ctx, out, in, scope.api, func() error {
				return scope.api.RemoveMember(ctx, scope.project.ID, user)
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "removed %s from project %s\n", user, scope.project.Name)
			return nil
		},
	}
	addAccessScopeFlags(command, &project, &remote)
	command.Flags().StringVar(&environment, "environment", "", "drop only the explicit role on this environment")
	command.Flags().BoolVar(&yes, "yes", false, "skip the confirmation")
	return command
}

// confirmError turns an unanswerable confirmation (no terminal, closed
// stdin) into the usual hint.
func confirmError(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return errors.New("non-interactive use requires --yes")
	}
	return err
}
