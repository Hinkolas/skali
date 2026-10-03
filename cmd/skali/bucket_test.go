package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseRetireAfter(t *testing.T) {
	t.Parallel()
	window, err := parseRetireAfter("90m")
	require.NoError(t, err)
	require.Equal(t, 90*time.Minute, window)
	window, err = parseRetireAfter(" 2d ")
	require.NoError(t, err)
	require.Equal(t, 48*time.Hour, window)
	for _, text := range []string{"30s", "0", "8d", "", "soon", "1500ms"} {
		_, err := parseRetireAfter(text)
		require.Error(t, err, text)
	}
	require.Equal(t, "1h", describeWindow(time.Hour))
	require.Equal(t, "90m", describeWindow(90*time.Minute))
	require.Equal(t, "7d", describeWindow(7*24*time.Hour))
}

// The summary names the bucket and the window; declining starts nothing;
// the default window is an hour, --retire-after overrides it, and a bad
// window is refused before the remote is asked anything.
func TestBucketRotateConfirmsWithSummary(t *testing.T) {
	install := seedBackupScope(t)
	rotate := func(stdin string, args ...string) (string, error) {
		return runCommand(t, newBucketCommand(), stdin, append([]string{"rotate", "--detach"}, args...)...)
	}

	out, err := rotate("n\n", "files")
	require.ErrorContains(t, err, "aborted")
	require.Contains(t, out, "project      flowdemo")
	require.Contains(t, out, "environment  production")
	require.Contains(t, out, "bucket       files (previous key retires after 1h)")
	require.Contains(t, out, "Rotate the keypair of bucket files in production? [Y/n]")
	require.Contains(t, out, "keep working for 1h, then fail")
	require.Empty(t, install.posts)

	out, err = rotate("\n", "files", "--environment", "staging", "--retire-after", "90m")
	require.NoError(t, err)
	require.Contains(t, out, "environment  staging")
	require.Contains(t, out, "previous key retires after 90m")
	require.Contains(t, out, "rotate the keypair of buckets.files in staging")
	require.Contains(t, out, "skali run attach run-rotate-1")
	require.Equal(t, []string{"rotate:p1-e2:files:5400"}, install.posts)

	_, err = rotate("", "files", "--retire-after", "30s")
	require.ErrorContains(t, err, `--retire-after "30s": must be between 1m and 7d`)
	_, err = rotate("", "files")
	require.ErrorContains(t, err, "non-interactive use requires --yes")
	out, err = rotate("", "files", "--yes")
	require.NoError(t, err)
	require.NotContains(t, out, "[Y/n]")
	require.Equal(t, []string{"rotate:p1-e2:files:5400", "rotate:p1-e1:files:3600"}, install.posts)
}

// The route needs a fresh session: the CLI reauthenticates and retries
// like every other sudo command.
func TestBucketRotateReauthenticates(t *testing.T) {
	install := seedBackupScope(t)
	install.reauthRequired = true
	out, err := runCommand(t, newBucketCommand(), "hunter2\n", "rotate", "files", "--yes", "--detach")
	require.NoError(t, err)
	require.Contains(t, out, "Confirm your password")
	require.Equal(t, []string{"rotate:p1-e1:files:3600"}, install.posts)
	require.Equal(t, 1, install.reauths)
}
