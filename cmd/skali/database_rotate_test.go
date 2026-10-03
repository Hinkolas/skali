package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The summary names the database and the window; declining starts
// nothing; the default window is an hour, --retire-after overrides it, and
// a bad window is refused before the remote is asked anything.
func TestDatabaseRotateConfirmsWithSummary(t *testing.T) {
	install := seedBackupScope(t)
	rotate := func(stdin string, args ...string) (string, error) {
		return runCommand(t, newDatabaseCommand(), stdin, append([]string{"rotate", "--detach"}, args...)...)
	}

	out, err := rotate("n\n", "data")
	require.ErrorContains(t, err, "aborted")
	require.Contains(t, out, "project      flowdemo")
	require.Contains(t, out, "environment  production")
	require.Contains(t, out, "database     data (previous login role retires after 1h)")
	require.Contains(t, out, "Rotate the credentials of database data in production? [Y/n]")
	require.Contains(t, out, "keep working for 1h; then they are terminated and the role is dropped")
	require.Empty(t, install.posts)

	out, err = rotate("\n", "data", "--environment", "staging", "--retire-after", "90m")
	require.NoError(t, err)
	require.Contains(t, out, "environment  staging")
	require.Contains(t, out, "previous login role retires after 90m")
	require.Contains(t, out, "rotate the credentials of databases.data in staging")
	require.Contains(t, out, "skali run attach run-rotate-db-1")
	require.Equal(t, []string{"rotate-db:p1-e2:data:5400"}, install.posts)

	_, err = rotate("", "data", "--retire-after", "8d")
	require.ErrorContains(t, err, `--retire-after "8d": must be between 1m and 7d`)
	_, err = rotate("", "data")
	require.ErrorContains(t, err, "non-interactive use requires --yes")
	out, err = rotate("", "data", "--yes")
	require.NoError(t, err)
	require.NotContains(t, out, "[Y/n]")
	require.Equal(t, []string{"rotate-db:p1-e2:data:5400", "rotate-db:p1-e1:data:3600"}, install.posts)
}

// The route needs a fresh session: the CLI reauthenticates and retries
// like every other sudo command.
func TestDatabaseRotateReauthenticates(t *testing.T) {
	install := seedBackupScope(t)
	install.reauthRequired = true
	out, err := runCommand(t, newDatabaseCommand(), "hunter2\n", "rotate", "data", "--yes", "--detach")
	require.NoError(t, err)
	require.Contains(t, out, "Confirm your password")
	require.Equal(t, []string{"rotate-db:p1-e1:data:3600"}, install.posts)
	require.Equal(t, 1, install.reauths)
}
