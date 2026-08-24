package installer

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/installer/host"
)

// unitHandler scripts systemctl is-enabled/is-active per unit; other verbs
// succeed and are inspected through fake.Commands.
func unitHandler(present, active map[string]bool) func(host.Command) (host.Result, error) {
	return func(cmd host.Command) (host.Result, error) {
		if len(cmd.Args) != 2 {
			return host.Result{}, nil
		}
		unit := cmd.Args[1]
		switch cmd.Args[0] {
		case "is-enabled":
			if present[unit] {
				return host.Result{Stdout: "enabled\n"}, nil
			}
			return host.Result{ExitCode: 1, Stdout: "not-found\n"}, nil
		case "is-active":
			if active[unit] {
				return host.Result{Stdout: "active\n"}, nil
			}
			return host.Result{ExitCode: 3, Stdout: "inactive\n"}, nil
		}
		return host.Result{}, nil
	}
}

func commandLines(fake *host.Fake) []string {
	var lines []string
	for _, cmd := range fake.Commands {
		lines = append(lines, strings.Join(append([]string{cmd.Name}, cmd.Args...), " "))
	}
	return lines
}

func TestEnsureStoragePrerequisitesFreshHost(t *testing.T) {
	t.Parallel()
	fake := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"systemctl": unitHandler(map[string]bool{}, map[string]bool{}),
		"apt-get":   func(host.Command) (host.Result, error) { return host.Result{}, nil },
	}}
	require.NoError(t, EnsureStoragePrerequisites(context.Background(), fake, nil))
	lines := commandLines(fake)
	require.Contains(t, lines, "apt-get update -qq")
	require.Contains(t, lines, "apt-get install -y open-iscsi")
	require.Contains(t, lines, "systemctl enable --now iscsid")
	// No multipathd on the host: nothing to blacklist.
	require.Empty(t, fake.Writes)
}

func TestEnsureStoragePrerequisitesAlreadyInstalled(t *testing.T) {
	t.Parallel()
	fake := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"systemctl": unitHandler(map[string]bool{"iscsid": true}, map[string]bool{"iscsid": true}),
	}}
	require.NoError(t, EnsureStoragePrerequisites(context.Background(), fake, nil))
	lines := commandLines(fake)
	for _, line := range lines {
		require.NotContains(t, line, "apt-get", "an installed host must not touch apt")
	}
	require.NotContains(t, lines, "systemctl enable --now iscsid",
		"an active iscsid must not be re-enabled")
	require.Empty(t, fake.Writes)
}

func TestEnsureStoragePrerequisitesMultipathd(t *testing.T) {
	t.Parallel()
	fake := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"systemctl": unitHandler(
			map[string]bool{"iscsid": true, "multipathd": true},
			map[string]bool{"iscsid": true, "multipathd": true},
		),
	}}
	require.NoError(t, EnsureStoragePrerequisites(context.Background(), fake, nil))
	data, err := fake.ReadFile(context.Background(), multipathBlacklistPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `devnode "^sd[a-z0-9]+"`)
	require.Contains(t, commandLines(fake), "systemctl restart multipathd")

	// A second run finds the blacklist and mutates nothing further.
	fake.Commands = nil
	require.NoError(t, EnsureStoragePrerequisites(context.Background(), fake, nil))
	for _, line := range commandLines(fake) {
		require.NotContains(t, line, "restart")
	}
}

func TestEnsureStoragePrerequisitesAptFailure(t *testing.T) {
	t.Parallel()
	fake := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"systemctl": unitHandler(map[string]bool{}, map[string]bool{}),
		"apt-get": func(cmd host.Command) (host.Result, error) {
			if cmd.Args[0] == "install" {
				return host.Result{ExitCode: 100, Stderr: "no network"}, nil
			}
			return host.Result{}, nil
		},
	}}
	err := EnsureStoragePrerequisites(context.Background(), fake, nil)
	require.ErrorContains(t, err, "apt-get install open-iscsi")
	require.ErrorContains(t, err, "no network")
	require.False(t, errors.Is(err, context.Canceled))
}
