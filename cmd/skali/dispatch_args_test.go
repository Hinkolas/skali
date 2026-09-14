package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPreparseArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want invocation
	}{
		{"remote flag", []string{"deploy", "--remote", "khz"}, invocation{command: "deploy", path: "deploy", remote: "khz"}},
		{"remote equals", []string{"deploy", "--remote=khz"}, invocation{command: "deploy", path: "deploy", remote: "khz"}},
		{"trailing remote", []string{"deploy", "--remote"}, invocation{command: "deploy", path: "deploy"}},
		{"short help", []string{"-h"}, invocation{help: true}},
		{"long help", []string{"deploy", "--help"}, invocation{command: "deploy", path: "deploy", help: true}},
		{"version flag", []string{"--version"}, invocation{version: true}},
		{"help command", []string{"help", "deploy"}, invocation{command: "deploy", path: "deploy", help: true}},
		{"completion request", []string{"__complete", "logs", ""}, invocation{command: "logs", path: "logs", completion: true}},
		{"double dash stops the scan", []string{"exec", "web", "--", "sh", "-c", "--remote", "x"}, invocation{command: "exec", path: "exec"}},
		{"manifest", []string{"deploy", "--manifest", "app/skali.yml"}, invocation{command: "deploy", path: "deploy", manifest: "app/skali.yml"}},
		{"manifest equals", []string{"validate", "--manifest=app/skali.yml"}, invocation{command: "validate", path: "validate", manifest: "app/skali.yml"}},
		{"verbose before the command", []string{"--verbose", "deploy"}, invocation{command: "deploy", path: "deploy", verbose: true}},
		{"persistent remote on a group", []string{"run", "list", "--remote", "khz"}, invocation{command: "run", path: "run list", remote: "khz"}},
		{"nested command", []string{"cluster", "status"}, invocation{command: "cluster", path: "cluster status"}},
		{"dev dispatches", []string{"dev", "start", "--force"}, invocation{command: "dev", path: "dev start"}},
		{"managed upgrade version", []string{"cluster", "upgrade", "--version", "v0.5.0"}, invocation{command: "cluster", path: "cluster upgrade"}},
		{"dev remote override", []string{"dev", "--remote", "khz"}, invocation{command: "dev", path: "dev", remote: "khz"}},
		{"skill read with since", []string{"skill", "read", "manifest", "--since", "v0.1.0-rc.2"}, invocation{command: "skill", path: "skill read"}},
		{"skill install", []string{"skill", "install", "--agent", "claude"}, invocation{command: "skill", path: "skill install"}},
		{"unknown command", []string{"frobnicate"}, invocation{command: "frobnicate", path: "frobnicate"}},
		{"bare", nil, invocation{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, preparseArgs(tc.args, newRootCommand))
		})
	}
}

// TestNoDispatchCommandsExist keeps the never-dispatch list honest: every
// entry but cobra's own help names a real top-level command.
func TestNoDispatchCommandsExist(t *testing.T) {
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
	for _, name := range []string{"deploy", "plan", "validate", "manifest", "run", "logs", "exec", "values", "backup", "env", "access", "rollback", "dev", "skill"} {
		require.False(t, noDispatchCommands[name], "%s must dispatch", name)
	}
	paths := map[string]bool{}
	for path := range noDispatchPaths {
		paths[path] = true
	}

	for path := range paths {
		found, _, err := newRootCommand().Find(strings.Fields(path))
		require.NoError(t, err, path)
		require.Equal(t, path, strings.TrimPrefix(found.CommandPath(), "skali "), "%q is not a command path", path)
	}
}

// Dispatch requires the first release implementing the shared context contract.
func TestUnsupportedDispatchRelease(t *testing.T) {
	require.True(t, unsupportedDispatchRelease("v0.1.0-rc.2"))
	require.True(t, unsupportedDispatchRelease("v0.1.0-alpha.8"))
	require.False(t, unsupportedDispatchRelease("v0.1.0-rc.3"))
	require.False(t, unsupportedDispatchRelease("v0.2.0"))
	require.True(t, unsupportedDispatchRelease("v0.1.0-alpha.1"))
}
