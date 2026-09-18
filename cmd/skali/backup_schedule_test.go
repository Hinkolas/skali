package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/client"
)

func TestBackupScheduleSetValidatesBeforeAsking(t *testing.T) {
	install := seedAccessScope(t)
	_, err := runCommand(t, newBackupCommand(), "", "schedule", "set", "--keep", "7d")
	require.ErrorContains(t, err, `required flag(s) "every" not set`)
	_, err = runCommand(t, newBackupCommand(), "", "schedule", "set", "--every", "0 3 * * *")
	require.ErrorContains(t, err, `required flag(s) "keep" not set`)
	_, err = runCommand(t, newBackupCommand(), "", "schedule", "set", "--every", "0 3 * *", "--keep", "7d")
	require.ErrorContains(t, err, `--every "0 3 * *": expected 5 fields`)
	_, err = runCommand(t, newBackupCommand(), "", "schedule", "set", "--every", "0 3 * * *", "--keep", "0")
	require.ErrorContains(t, err, `--keep "0": must be a positive duration`)
	_, err = runCommand(t, newBackupCommand(), "", "schedule", "set", "--every", "0 3 * * *", "--keep", "soon")
	require.ErrorContains(t, err, `--keep "soon": must be a positive duration`)
	_, err = runCommand(t, newBackupCommand(), "", "schedule", "set", "--every", "0 3 * * *", "--keep", "1500ms")
	require.ErrorContains(t, err, "whole seconds")
	require.Empty(t, install.posts, "nothing reaches the server before the flags parse")
}

func TestBackupScheduleSetShowRemove(t *testing.T) {
	install := seedAccessScope(t)

	// Off by default.
	out, err := runCommand(t, newBackupCommand(), "", "schedule", "show")
	require.NoError(t, err)
	require.Contains(t, out, "environment    production (flowdemo on r)")
	require.Contains(t, out, "backups        off")
	require.Contains(t, out, "turn them on with skali backup schedule set")

	// Turning them on: the summary names the change, asks, and the settings
	// are printed back with the next fire.
	out, err = runCommand(t, newBackupCommand(), "\n", "schedule", "set", "--every", "0 3 * * *", "--keep", "7d")
	require.NoError(t, err)
	require.Contains(t, out, "environment  production")
	require.Contains(t, out, "backups        off -> daily at 03:00 UTC, keep 7 days")
	require.Contains(t, out, "Schedule automatic backups of production? [Y/n]")
	require.Contains(t, out, "snapshots the schedule takes expire after 7 days")
	require.Contains(t, out, "environment    production (flowdemo on r)")
	require.Contains(t, out, "backups        daily at 03:00 UTC, keep 7 days")
	require.Contains(t, out, "next backup ")
	install.mu.Lock()
	require.Equal(t, &client.BackupSchedule{Schedule: "0 3 * * *", RetentionSeconds: 7 * 86400, Strategy: "complete"},
		install.envs["p1"][0].Settings.Backup)
	install.mu.Unlock()
	require.Equal(t, []string{"settings:p1-e1"}, install.posts)

	out, err = runCommand(t, newBackupCommand(), "", "schedule", "show")
	require.NoError(t, err)
	require.Contains(t, out, "schedule       daily at 03:00 UTC (0 3 * * *)")
	require.Contains(t, out, "keep           7 days")
	require.Contains(t, out, "next           ")
	require.Contains(t, out, "skali backup list --environment production")

	// A changed schedule on another environment, without asking; the
	// expression is normalized.
	out, err = runCommand(t, newBackupCommand(), "", "schedule", "set", "--yes", "--environment", "staging",
		"--every", "0  */6 * * *", "--keep", "36h")
	require.NoError(t, err)
	require.Contains(t, out, "environment    staging")
	require.Contains(t, out, "backups        every 6 hours UTC, keep 36 hours")
	install.mu.Lock()
	require.Equal(t, "0 */6 * * *", install.envs["p1"][1].Settings.Backup.Schedule)
	require.EqualValues(t, 36*3600, install.envs["p1"][1].Settings.Backup.RetentionSeconds)
	install.mu.Unlock()

	// Declining changes nothing; an unanswerable prompt is refused.
	_, err = runCommand(t, newBackupCommand(), "n\n", "schedule", "set", "--every", "0 4 * * *", "--keep", "1d")
	require.ErrorContains(t, err, "aborted")
	_, err = runCommand(t, newBackupCommand(), "", "schedule", "set", "--every", "0 4 * * *", "--keep", "1d")
	require.ErrorContains(t, err, "non-interactive use requires --yes")
	require.Equal(t, []string{"settings:p1-e1", "settings:p1-e2"}, install.posts)

	// Turning them off: declined, then confirmed; already-off short-circuits.
	_, err = runCommand(t, newBackupCommand(), "n\n", "schedule", "remove")
	require.ErrorContains(t, err, "aborted")
	_, err = runCommand(t, newBackupCommand(), "\n", "schedule", "remove")
	require.ErrorContains(t, err, "aborted", "the question defaults to no")
	out, err = runCommand(t, newBackupCommand(), "y\n", "schedule", "remove")
	require.NoError(t, err)
	require.Contains(t, out, "backups        daily at 03:00 UTC, keep 7 days -> off")
	require.Contains(t, out, "Turn off automatic backups of production? [y/N]")
	require.Contains(t, out, "automatic backups off")
	install.mu.Lock()
	require.Nil(t, install.envs["p1"][0].Settings.Backup)
	install.mu.Unlock()
	out, err = runCommand(t, newBackupCommand(), "", "schedule", "rm")
	require.NoError(t, err)
	require.Contains(t, out, "automatic backups of production are already off")
	require.Equal(t, []string{"settings:p1-e1", "settings:p1-e2", "settings:p1-e1"}, install.posts)
}

// The settings route needs a fresh session: the CLI reauthenticates and
// retries like every other sudo command.
func TestBackupScheduleSetReauthenticates(t *testing.T) {
	install := seedAccessScope(t)
	install.reauthRequired = true
	out, err := runCommand(t, newBackupCommand(), "hunter2\n", "schedule", "set", "--yes", "--every", "0 3 * * *", "--keep", "7d")
	require.NoError(t, err)
	require.Contains(t, out, "Confirm your password")
	require.Contains(t, out, "backups        daily at 03:00 UTC, keep 7 days")
}

func TestDescribeRetention(t *testing.T) {
	require.Equal(t, "7 days", describeRetention(7*86400))
	require.Equal(t, "1 day", describeRetention(86400))
	require.Equal(t, "36 hours", describeRetention(36*3600))
	require.Equal(t, "2 minutes", describeRetention(120))
	require.Equal(t, "1 minute", describeRetention(60))
	require.Equal(t, "7d", shortRetention(7*86400))
	require.Equal(t, "36h", shortRetention(36*3600))
	require.Equal(t, "2m", shortRetention(120))
}
