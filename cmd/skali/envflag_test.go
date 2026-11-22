package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// --environment is gone; passing it names the flag that replaced it.
func TestRenamedEnvironmentFlag(t *testing.T) {
	for _, args := range [][]string{
		{"backup", "list", "--environment", "staging"},
		{"deploy", "--environment=staging"},
		{"route", "list", "--environment", "staging"},
	} {
		root := newRootCommand()
		root.SetArgs(args)
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		err := root.ExecuteContext(context.Background())
		require.EqualError(t, err, "--environment was renamed to --env", "%v", args)
	}

	root := newRootCommand()
	root.SetArgs([]string{"backup", "list", "--bogus"})
	root.SetErr(&bytes.Buffer{})
	require.EqualError(t, root.ExecuteContext(context.Background()), "unknown flag: --bogus")
}

func TestArgsForRelease(t *testing.T) {
	args := []string{"deploy", "--env", "production", "--env=staging", "--env-file", ".env", "--", "--env", "x"}
	older := []string{"deploy", "--environment", "production", "--environment=staging", "--env-file", ".env", "--", "--env", "x"}
	require.Equal(t, older, argsForRelease(args, "v0.1.4-rc.3"))
	require.Equal(t, older, argsForRelease(args, "v0.1.3"))
	require.Equal(t, args, argsForRelease(args, envFlagRelease))
	require.Equal(t, args, argsForRelease(args, "v0.1.4"))
	require.Equal(t, args, argsForRelease(args, "v0.0.0-dev"))
	// The caller's slice is left as it was.
	require.Equal(t, "--env", args[1])
}
