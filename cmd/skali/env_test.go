package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEnvLsRendersSettings(t *testing.T) {
	install := seedAccessScope(t)
	created := time.Date(2026, 8, 19, 10, 30, 0, 0, time.Local)
	install.mu.Lock()
	install.envs["p1"][0].CreatedAt = &created
	install.envs["p1"][1].Access = "none"
	install.envs["p1"][1].Settings = nil
	install.envs["p1"][0].Settings.DeployPolicy = "promote-only"
	install.envs["p1"][0].Settings.PromoteFrom = []string{"staging"}
	install.envs["p1"][0].Settings.Priority = "high"
	install.envs["p1"][0].Settings.MaxRole = "read"
	install.mu.Unlock()

	out, err := runCommand(t, newEnvCommand(), "", "ls")
	require.NoError(t, err)
	require.Contains(t, out, "project  flowdemo (r)")
	require.Regexp(t, `(?m)^NAME +ACCESS +PRIORITY +POLICY +CEILING +CREATED$`, out)
	require.Regexp(t, `(?m)^production \(bound\) +admin +high +promote-only \(from staging\) +read +2026-08-19 10:30$`, out)
	require.Regexp(t, `(?m)^staging +locked +- +- +- +-$`, out)
}

func TestEnvCreatePassesPriority(t *testing.T) {
	install := seedAccessScope(t)
	out, err := runCommand(t, newEnvCommand(), "", "create", "feature", "--yes")
	require.NoError(t, err)
	require.Contains(t, out, "project      flowdemo")
	require.Contains(t, out, "environment  feature (new)")
	require.Contains(t, out, "created environment feature in project flowdemo")
	require.Contains(t, out, "priority normal, ceiling admin, deploy policy direct")
	out, err = runCommand(t, newEnvCommand(), "\n", "create", "prod2", "--priority", "high")
	require.NoError(t, err)
	require.Contains(t, out, "Create environment prod2 in project flowdemo? [Y/n]")
	require.Contains(t, out, "High priority keeps it running")
	require.NoError(t, err)
	require.Contains(t, out, "priority high, ceiling read")
	require.Contains(t, out, "consider skali env set --deploy-policy promote-only")
	_, err = runCommand(t, newEnvCommand(), "", "create", "x", "--priority", "urgent")
	require.ErrorContains(t, err, "--priority must be normal or high")
	// Declining, and an unanswerable prompt, create nothing.
	_, err = runCommand(t, newEnvCommand(), "n\n", "create", "nope")
	require.ErrorContains(t, err, "aborted")
	_, err = runCommand(t, newEnvCommand(), "", "create", "nope")
	require.ErrorContains(t, err, "non-interactive use requires --yes")
	require.Equal(t, []string{"environment:p1:feature", "environment:p1:prod2"}, install.posts)
}

func TestEnvSetBuildsPatch(t *testing.T) {
	install := seedAccessScope(t)
	_, err := runCommand(t, newEnvCommand(), "", "set")
	require.ErrorContains(t, err, "nothing to change")
	_, err = runCommand(t, newEnvCommand(), "", "set", "--max-role", "owner")
	require.ErrorContains(t, err, "--max-role must be one of none, read, deploy, maintain, admin")

	// The bound environment is the default target.
	out, err := runCommand(t, newEnvCommand(), "\n", "set", "--max-role", "read",
		"--deploy-policy", "promote-only", "--promote-from", "staging, qa")
	require.NoError(t, err)
	// The summary names the target and every change before asking.
	require.Contains(t, out, "environment  production")
	require.Contains(t, out, "max role       admin -> read")
	require.Contains(t, out, "deploy policy  direct -> promote-only")
	require.Contains(t, out, "promote from   any -> staging, qa")
	require.Contains(t, out, "Change environment production? [Y/n]")
	require.Contains(t, out, "environment    production (flowdemo on r)")
	require.Contains(t, out, "max role       read")
	require.Contains(t, out, "deploy policy  promote-only (from staging, qa)")
	require.Contains(t, out, "priority       normal")
	// --promote-from any clears the list; --environment picks another.
	out, err = runCommand(t, newEnvCommand(), "", "set", "--yes", "--environment", "staging", "--promote-from", "any", "--deploy-policy", "promote-only")
	require.NoError(t, err)
	require.Contains(t, out, "environment    staging")
	require.Contains(t, out, "deploy policy  promote-only (from any)")
	install.mu.Lock()
	require.Equal(t, []string{"staging", "qa"}, install.envs["p1"][0].Settings.PromoteFrom)
	require.Equal(t, []string{}, install.envs["p1"][1].Settings.PromoteFrom)
	install.mu.Unlock()
	require.Equal(t, []string{"settings:p1-e1", "settings:p1-e2"}, install.posts)

	// A priority change names the class the application pods roll onto; an
	// unchanged priority does not.
	out, err = runCommand(t, newEnvCommand(), "", "set", "--yes", "--priority", "high")
	require.NoError(t, err)
	require.Contains(t, out, "priority       normal -> high")
	require.Contains(t, out, "priority       high")
	require.Contains(t, out, "application pods roll onto priority class skali-high")
	out, err = runCommand(t, newEnvCommand(), "", "set", "--yes", "--priority", "high")
	require.NoError(t, err)
	require.NotContains(t, out, "application pods roll")

	// Declining changes nothing.
	posts := len(install.posts)
	_, err = runCommand(t, newEnvCommand(), "n\n", "set", "--priority", "normal")
	require.ErrorContains(t, err, "aborted")
	require.Len(t, install.posts, posts)
}

func TestEnvRmPurgesAfterConfirmation(t *testing.T) {
	install := seedAccessScope(t)
	_, err := runCommand(t, newEnvCommand(), "n\n", "rm", "staging")
	require.ErrorContains(t, err, "aborted")
	_, err = runCommand(t, newEnvCommand(), "", "rm", "staging")
	require.ErrorContains(t, err, "non-interactive use requires --yes")
	_, err = runCommand(t, newEnvCommand(), "", "rm", "nothing", "--yes")
	require.ErrorContains(t, err, "environment nothing does not exist")
	require.Empty(t, install.posts)

	out, err := runCommand(t, newEnvCommand(), "y\n", "rm", "staging")
	require.NoError(t, err)
	require.Contains(t, out, "This destroys environment staging of project flowdemo on r completely:")
	require.Contains(t, out, "purge started  run-p1-e2")
	require.Contains(t, out, "skali run attach run-p1-e2")
	install.reauthRequired = true
	out, err = runCommand(t, newEnvCommand(), "hunter2\n", "rm", "production", "--yes", "--remote", "r")
	require.NoError(t, err)
	require.Contains(t, out, "Confirm your password")
	require.Contains(t, out, "skali run attach --remote r run-p1-e1")
	require.Equal(t, []string{"teardown:p1-e2:true", "teardown:p1-e1:true"}, install.posts)
}
