package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestReleaseTaskLocalTagGuard(t *testing.T) {
	data, err := os.ReadFile("../../Taskfile.yml")
	require.NoError(t, err)
	var taskfile struct {
		Tasks map[string]struct {
			Preconditions []struct{ Sh, Msg string }
		}
	}
	require.NoError(t, yaml.Unmarshal(data, &taskfile))
	var guard string
	for _, precondition := range taskfile.Tasks["release:tag"].Preconditions {
		if precondition.Msg == "tag {{.V}} already exists locally" {
			guard = strings.ReplaceAll(precondition.Sh, "{{.V}}", "v0.1.0-alpha.1")
		}
	}
	require.NotEmpty(t, guard, "the release task must check for an existing local tag")

	// Execute the YAML-decoded command in a disposable repository. An
	// unquoted leading ! is swallowed by YAML and reverses both outcomes.
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", output)
	}
	git("init", "-q")
	git("-c", "user.name=Release test", "-c", "user.email=test@example.invalid",
		"-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null",
		"commit", "--allow-empty", "-qm", "release fixture")
	check := func() error {
		cmd := exec.Command("sh", "-c", guard)
		cmd.Dir = dir
		return cmd.Run()
	}
	require.NoError(t, check(), "a missing tag must allow the release to proceed")
	git("-c", "tag.gpgsign=false", "tag", "v0.1.0-alpha.1")
	require.Error(t, check(), "an existing tag must block the release")
}
