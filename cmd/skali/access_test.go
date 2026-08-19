package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/checkout"
	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/client"
)

// seedAccessScope stages a bound checkout whose project has two
// environments, two members, and one explicit cell.
func seedAccessScope(t *testing.T) *fakeInstall {
	t.Helper()
	install := newFakeInstall(t)
	install.seed("p1", "flowdemo", "production", "staging")
	cell := "deploy"
	install.members["p1"] = []client.Member{
		{UserID: "u-bob", Email: "bob@example.com", Role: "read", Environments: map[string]client.MemberEnvironment{
			"production": {Role: "read"},
			"staging":    {Role: "deploy", Cell: &cell},
		}},
		{UserID: "u-root", Email: "root@example.com", Role: "read", InstanceAdmin: true, Environments: map[string]client.MemberEnvironment{
			"production": {Role: "admin"},
			"staging":    {Role: "admin"},
		}},
	}
	stageRemotes(t, "r", map[string]*cliconfig.Remote{"r": {Master: install.srv.URL}})
	project := testFlowProject(t)
	require.NoError(t, checkout.Save(project.Root, &checkout.Target{
		Master: install.srv.URL, Project: "flowdemo", Environment: "production"}))
	t.Chdir(project.Root)
	return install
}

// runCommand executes a command with piped stdin and returns its output;
// the error is returned for the caller to judge.
func runCommand(t *testing.T, command *cobra.Command, stdin string, args ...string) (string, error) {
	t.Helper()
	command.SetArgs(args)
	out := &bytes.Buffer{}
	command.SetOut(out)
	command.SetErr(out)
	command.SetIn(strings.NewReader(stdin))
	err := command.ExecuteContext(context.Background())
	return out.String(), err
}

func TestAccessLsRendersGrid(t *testing.T) {
	install := seedAccessScope(t)
	out, err := runCommand(t, newAccessCommand(), "", "ls")
	require.NoError(t, err)
	require.Contains(t, out, "project  flowdemo (r)  your role: admin")
	require.Regexp(t, `(?m)^MEMBER +PROJECT +production +staging$`, out)
	require.Regexp(t, `(?m)^bob@example.com +read +read +deploy\*$`, out)
	require.Regexp(t, `(?m)^root@example.com +read \(instance admin\) +admin +admin$`, out)
	require.Contains(t, out, "* explicit per-environment role")

	// A locked environment: the header says so, the cells show "-".
	install.mu.Lock()
	install.envs["p1"][0].Access = "none"
	install.envs["p1"][0].Settings = nil
	for i := range install.members["p1"] {
		delete(install.members["p1"][i].Environments, "production")
	}
	install.mu.Unlock()
	out, err = runCommand(t, newAccessCommand(), "", "ls")
	require.NoError(t, err)
	require.Regexp(t, `(?m)^MEMBER +PROJECT +production \(locked\) +staging$`, out)
	require.Regexp(t, `(?m)^bob@example.com +read +- +deploy\*$`, out)

	install.mu.Lock()
	install.members["p1"] = nil
	install.mu.Unlock()
	out, err = runCommand(t, newAccessCommand(), "", "ls")
	require.NoError(t, err)
	require.Contains(t, out, "no members; instance admins see every project without membership")
}

func TestAccessSetRoutesMembershipAndCells(t *testing.T) {
	install := seedAccessScope(t)
	out, err := runCommand(t, newAccessCommand(), "", "set", "carol@example.com", "maintain")
	require.NoError(t, err)
	require.Contains(t, out, "carol@example.com  maintain on project flowdemo")
	out, err = runCommand(t, newAccessCommand(), "", "set", "carol@example.com", "none", "--environment", "production")
	require.NoError(t, err)
	require.Contains(t, out, "carol@example.com  none on environment production")
	require.Equal(t, []string{"member:p1:carol@example.com:maintain", "cell:p1-e1:carol@example.com:none"}, install.posts)

	// Local refusals before any round trip, and the membership hint on 409.
	_, err = runCommand(t, newAccessCommand(), "", "set", "carol@example.com", "none")
	require.ErrorContains(t, err, "none is not a project role")
	_, err = runCommand(t, newAccessCommand(), "", "set", "carol@example.com", "owner")
	require.ErrorContains(t, err, "role must be one of read, deploy, maintain, admin")
	_, err = runCommand(t, newAccessCommand(), "", "set", "dave@example.com", "deploy", "--environment", "staging")
	require.ErrorContains(t, err, "grant a project role first: skali access set dave@example.com read")
	_, err = runCommand(t, newAccessCommand(), "", "set", "dave@example.com", "deploy", "--environment", "nothing")
	require.ErrorContains(t, err, "environment nothing does not exist in project flowdemo")
}

func TestAccessRmConfirmsMembership(t *testing.T) {
	install := seedAccessScope(t)
	// Cells drop without a question; memberships ask, honoring y/N and --yes.
	out, err := runCommand(t, newAccessCommand(), "", "rm", "bob@example.com", "--environment", "staging")
	require.NoError(t, err)
	require.Contains(t, out, "removed the explicit role of bob@example.com on environment staging")
	_, err = runCommand(t, newAccessCommand(), "n\n", "rm", "bob@example.com")
	require.ErrorContains(t, err, "aborted")
	_, err = runCommand(t, newAccessCommand(), "", "rm", "bob@example.com")
	require.ErrorContains(t, err, "non-interactive use requires --yes")
	out, err = runCommand(t, newAccessCommand(), "y\n", "rm", "bob@example.com")
	require.NoError(t, err)
	require.Contains(t, out, "Remove bob@example.com from project flowdemo?")
	require.Contains(t, out, "removed bob@example.com from project flowdemo")
	out, err = runCommand(t, newAccessCommand(), "", "rm", "root@example.com", "--yes")
	require.NoError(t, err)
	require.NotContains(t, out, "Remove root@example.com")
	require.Equal(t, []string{"cell-rm:p1-e2:bob@example.com", "member-rm:p1:bob@example.com", "member-rm:p1:root@example.com"}, install.posts)
}

func TestAccessSetReauthenticatesOnce(t *testing.T) {
	install := seedAccessScope(t)
	install.reauthRequired = true
	// The password comes from the piped stdin after the gate refuses once.
	out, err := runCommand(t, newAccessCommand(), "hunter2\n", "set", "carol@example.com", "read")
	require.NoError(t, err)
	require.Contains(t, out, "Confirm your password")
	require.Contains(t, out, "carol@example.com  read on project flowdemo")
	require.Equal(t, 1, install.reauths)

	// A two-factor account is asked for its code instead.
	install.reauthRequired, install.twoFactor = true, true
	out, err = runCommand(t, newAccessCommand(), "123456\n", "set", "carol@example.com", "deploy")
	require.NoError(t, err)
	require.Contains(t, out, "Two-factor code")
	require.Equal(t, 2, install.reauths)

	// Nothing on stdin: the hint names the fix.
	install.reauthRequired, install.twoFactor = true, false
	_, err = runCommand(t, newAccessCommand(), "", "set", "carol@example.com", "deploy")
	require.ErrorContains(t, err, "confirm your password on stdin or re-run in a terminal")
}

func TestAccessProjectFlagOverridesCheckout(t *testing.T) {
	install := seedAccessScope(t)
	install.seed("p2", "other", "production")
	install.members["p2"] = []client.Member{{Email: "eve@example.com", Role: "admin",
		Environments: map[string]client.MemberEnvironment{"production": {Role: "admin"}}}}
	out, err := runCommand(t, newAccessCommand(), "", "ls", "--project", "other")
	require.NoError(t, err)
	require.Contains(t, out, "project  other (r)")
	require.Contains(t, out, "eve@example.com")
	require.NotContains(t, out, "bob@example.com")
	_, err = runCommand(t, newAccessCommand(), "", "ls", "--project", "missing")
	require.ErrorContains(t, err, "project missing does not exist")
}
