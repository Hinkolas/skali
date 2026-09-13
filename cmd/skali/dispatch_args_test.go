package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPreparseArgs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args []string
		want invocation
	}{
		{"remote flag", []string{"deploy", "--remote", "khz"}, invocation{command: "deploy", remote: "khz"}},
		{"remote equals", []string{"deploy", "--remote=khz"}, invocation{command: "deploy", remote: "khz"}},
		{"trailing remote", []string{"deploy", "--remote"}, invocation{command: "deploy"}},
		{"short help", []string{"-h"}, invocation{help: true}},
		{"long help", []string{"deploy", "--help"}, invocation{command: "deploy", help: true}},
		{"version flag", []string{"--version"}, invocation{version: true}},
		{"help command", []string{"help", "deploy"}, invocation{command: "help", help: true}},
		{"completion request", []string{"__complete", "logs", ""}, invocation{command: "completion"}},
		{"double dash stops the scan", []string{"exec", "web", "--", "sh", "-c", "--remote", "x"}, invocation{command: "exec"}},
		{"manifest", []string{"deploy", "--manifest", "app/skali.yml"}, invocation{command: "deploy", manifest: "app/skali.yml"}},
		{"manifest equals", []string{"validate", "--manifest=app/skali.yml"}, invocation{command: "validate", manifest: "app/skali.yml"}},
		{"verbose before the command", []string{"--verbose", "deploy"}, invocation{command: "deploy", verbose: true}},
		{"persistent remote on a group", []string{"run", "list", "--remote", "khz"}, invocation{command: "run", remote: "khz"}},
		{"nested command", []string{"cluster", "status"}, invocation{command: "cluster"}},
		{"unknown command", []string{"frobnicate"}, invocation{}},
		{"bare", nil, invocation{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, preparseArgs(tc.args, newRootCommand))
		})
	}
}

// TestNoDispatchCommandsExist keeps the never-dispatch list honest: every
// entry but cobra's own help names a real top-level command.
func TestNoDispatchCommandsExist(t *testing.T) {
	t.Parallel()
	names := map[string]bool{}
	for _, command := range newRootCommand().Commands() {
		names[command.Name()] = true
	}
	for name := range noDispatchCommands {
		if name == "help" {
			continue
		}
		require.True(t, names[name], "noDispatchCommands names %q, which is not a command", name)
	}
	for _, name := range []string{"deploy", "plan", "validate", "run", "logs", "exec", "values", "backup", "env", "access", "rollback"} {
		require.False(t, noDispatchCommands[name], "%s must dispatch", name)
	}
}
