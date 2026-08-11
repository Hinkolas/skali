package main

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// parseExecInvocation runs args through a real cobra parse so
// ArgsLenAtDash carries the -- position, like a live invocation.
func parseExecInvocation(t *testing.T, args []string) (string, []string, error) {
	t.Helper()
	var service string
	var argv []string
	var parseErr error
	command := &cobra.Command{
		Use: "exec", Args: cobra.ArbitraryArgs, SilenceUsage: true, SilenceErrors: true,
		RunE: func(command *cobra.Command, args []string) error {
			service, argv, parseErr = parseExecArgs(command, args)
			return nil
		},
	}
	var inv execInvocation
	addExecFlags(command, &inv)
	command.SetArgs(args)
	require.NoError(t, command.Execute())
	return service, argv, parseErr
}

func TestParseExecArgs(t *testing.T) {
	service, argv, err := parseExecInvocation(t, []string{"web", "--", "bun", "run", "seed:demo"})
	require.NoError(t, err)
	require.Equal(t, "web", service)
	require.Equal(t, []string{"bun", "run", "seed:demo"}, argv)

	// No service before the dash: the default-service path picks one.
	service, argv, err = parseExecInvocation(t, []string{"--", "sh", "-c", "echo hi"})
	require.NoError(t, err)
	require.Empty(t, service)
	require.Equal(t, []string{"sh", "-c", "echo hi"}, argv)

	// Service only: the server default shell applies.
	service, argv, err = parseExecInvocation(t, []string{"web"})
	require.NoError(t, err)
	require.Equal(t, "web", service)
	require.Empty(t, argv)

	// Nothing at all is valid too (default service, default shell).
	service, argv, err = parseExecInvocation(t, nil)
	require.NoError(t, err)
	require.Empty(t, service)
	require.Empty(t, argv)

	// Command flags survive behind the dash.
	service, argv, err = parseExecInvocation(t, []string{"-t", "web", "--", "ls", "-la"})
	require.NoError(t, err)
	require.Equal(t, "web", service)
	require.Equal(t, []string{"ls", "-la"}, argv)

	// A bare multi-word tail without the dash is ambiguous.
	_, _, err = parseExecInvocation(t, []string{"web", "sh"})
	require.ErrorContains(t, err, "separate the command with --")
}

func TestDecideTTY(t *testing.T) {
	// Tests never run on a terminal, so the matrix reduces to the
	// non-terminal rows; the terminal rows are covered manually.
	tty, err := decideTTY(execInvocation{})
	require.NoError(t, err)
	require.False(t, tty, "no terminal, no TTY")

	tty, err = decideTTY(execInvocation{noTTY: true})
	require.NoError(t, err)
	require.False(t, tty)

	_, err = decideTTY(execInvocation{forceTTY: true})
	require.ErrorContains(t, err, "not a TTY")

	tty, err = decideTTY(execInvocation{forceTTY: true, noTTY: true})
	require.NoError(t, err)
	require.False(t, tty, "--no-tty wins over -t")
}
