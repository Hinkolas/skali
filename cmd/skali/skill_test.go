package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/manifest"
	"github.com/Hinkolas/skali/internal/skill"
	versionpkg "github.com/Hinkolas/skali/internal/version"
)

func TestSelectAgentsEmptyInputKeepsAllPreselected(t *testing.T) {
	session, out := networkPromptFixture(t, "\n")

	agents, err := selectAgents(context.Background(), session, nil, false)
	require.NoError(t, err)
	require.Len(t, agents, 2)
	require.Equal(t, "claude", agents[0].Name)
	require.Equal(t, "codex", agents[1].Name)

	output := promptOutput(t, out)
	require.Contains(t, output, "Install the skali skill for which agents?")
	require.Contains(t, output, "Claude Code")
	require.Contains(t, output, "Codex")
	require.Contains(t, output, "~/.claude/skills/skali")
	require.Contains(t, output, "~/.agents/skills/skali")
}

func TestSelectAgentsPicksSingleAgent(t *testing.T) {
	session, _ := networkPromptFixture(t, "2\n")

	agents, err := selectAgents(context.Background(), session, nil, false)
	require.NoError(t, err)
	require.Len(t, agents, 1)
	require.Equal(t, "codex", agents[0].Name)
}

func TestSelectAgentsFlagsSkipThePrompt(t *testing.T) {
	agents, err := selectAgents(context.Background(), nil, []string{"codex", "claude", "codex"}, false)
	require.NoError(t, err)
	require.Len(t, agents, 2)
	require.Equal(t, "codex", agents[0].Name)
	require.Equal(t, "claude", agents[1].Name)

	all, err := selectAgents(context.Background(), nil, nil, true)
	require.NoError(t, err)
	require.Len(t, all, len(skill.Agents()))
}

func TestSelectAgentsRejectsUnknownFlagName(t *testing.T) {
	_, err := selectAgents(context.Background(), nil, []string{"cursor"}, false)
	require.ErrorContains(t, err, `unknown agent "cursor"`)
}

func TestInstallSkillWritesAndRenders(t *testing.T) {
	home := t.TempDir()
	out, err := os.CreateTemp(t.TempDir(), "install-*.txt")
	require.NoError(t, err)
	t.Cleanup(func() { _ = out.Close() })

	require.NoError(t, installSkill(out, home, skill.Agents()))

	output := promptOutput(t, out)
	require.Contains(t, output, "installed Claude Code skill to ~/.claude/skills/skali")
	require.Contains(t, output, "installed Codex skill to ~/.agents/skills/skali")
	require.Contains(t, output, "Restart agent sessions to pick up the new skill.")
	require.FileExists(t, filepath.Join(home, ".claude", "skills", "skali", "SKILL.md"))
	require.FileExists(t, filepath.Join(home, ".agents", "skills", "skali", "SKILL.md"))
}

func TestSkillReadListsTopics(t *testing.T) {
	out, err := runCapturingStdout(t, func() error { return execute(newRootCommand(), "skill", "read") })
	require.NoError(t, err)
	require.Contains(t, out, "references of skali "+versionpkg.Version)
	require.Contains(t, out, "manifest  every skali.yaml field")
	require.Contains(t, out, "cli       the skali commands")
	require.Contains(t, out, "run skali skill read <topic> from the project directory")
}

func TestSkillReadPrintsReferenceVerbatim(t *testing.T) {
	for _, topic := range skill.TopicNames() {
		out, err := runCapturingStdout(t, func() error { return execute(newRootCommand(), "skill", "read", topic) })
		require.NoError(t, err, topic)
		want, ok := skill.Reference(topic)
		require.True(t, ok)
		require.Equal(t, string(want), out, topic)
	}
	_, err := runCapturingStdout(t, func() error { return execute(newRootCommand(), "skill", "read", "architecture") })
	require.ErrorContains(t, err, `unknown topic "architecture" (valid: manifest, cli)`)
}

func TestSkillReadSince(t *testing.T) {
	out, err := runCapturingStdout(t, func() error {
		return execute(newRootCommand(), "skill", "read", "manifest", "--since", "v0.1.0-rc.2")
	})
	require.NoError(t, err)
	require.Contains(t, out, "manifest changes since v0.1.0-rc.2 (this skali is "+versionpkg.Version+")")
	require.Contains(t, out, "  v0.1.0-rc.3  removed  version\n")
	require.Contains(t, out, "    version was replaced by skali")
	require.Contains(t, out, "    fix: run skali manifest upgrade")

	out, err = runCapturingStdout(t, func() error {
		return execute(newRootCommand(), "skill", "read", "manifest", "--since", "0.1.0-rc.3")
	})
	require.NoError(t, err)
	require.Equal(t, "no manifest changes since v0.1.0-rc.3; this skali is "+versionpkg.Version+"\n", out)

	_, err = runCapturingStdout(t, func() error {
		return execute(newRootCommand(), "skill", "read", "manifest", "--since", "latest")
	})
	require.ErrorContains(t, err, `--since "latest" is not a skali release; expected a tag like `+manifest.ReferenceRelease())
	_, err = runCapturingStdout(t, func() error {
		return execute(newRootCommand(), "skill", "read", "cli", "--since", "v0.1.0-rc.2")
	})
	require.ErrorContains(t, err, "--since applies to the manifest topic")
	_, err = runCapturingStdout(t, func() error {
		return execute(newRootCommand(), "skill", "read", "--since", "v0.1.0-rc.2")
	})
	require.ErrorContains(t, err, "--since applies to the manifest topic")
}

func TestRenderChangesSinceListsEveryEntry(t *testing.T) {
	var out bytes.Buffer
	renderChangesSince(&out, "v0.1.0", "v0.2.0", []manifest.Change{
		{Release: "v0.1.1", Kind: manifest.ChangeAdded, Path: "applications.*.dev", Message: "dev blocks exist"},
		{Release: "v0.2.0", Kind: manifest.ChangeChanged, Path: "applications.*.rollout", Message: "the default is blue-green", Hint: "declare strategy: rolling to keep the old behavior"},
	})
	require.Equal(t, strings.Join([]string{
		"manifest changes since v0.1.0 (this skali is v0.2.0)",
		"  v0.1.1  added  applications.*.dev",
		"    dev blocks exist",
		"  v0.2.0  changed  applications.*.rollout",
		"    the default is blue-green",
		"    fix: declare strategy: rolling to keep the old behavior",
		"",
	}, "\n"), out.String())
}

func TestRefreshSkillSkipsWhenNotInstalled(t *testing.T) {
	home := t.TempDir()
	var out bytes.Buffer
	require.Empty(t, refreshSkill(context.Background(), clirender.NewTasks(&out), "/nonexistent/skali", home))
	require.Empty(t, out.String())
	entries, err := os.ReadDir(home)
	require.NoError(t, err)
	require.Empty(t, entries, "nothing is installed anew")
	require.Empty(t, refreshSkill(context.Background(), clirender.NewTasks(&out), "/nonexistent/skali", ""))
}

func TestRefreshSkillRunsInstallForManagedAgents(t *testing.T) {
	home := t.TempDir()
	// A managed Claude Code install and an unmanaged Codex one: only the
	// former is refreshed.
	claude, codex := skill.Agents()[0], skill.Agents()[1]
	_, err := skill.Install(home, claude)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(codex.Dir(home), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(codex.Dir(home), "SKILL.md"), []byte("---\nname: skali\n---\nmine\n"), 0o644))

	argv := filepath.Join(t.TempDir(), "argv")
	executable := writeExecutable(t, t.TempDir(), "skali", []byte("#!/bin/sh\necho \"$@\" > "+argv+"\n"))
	var out bytes.Buffer
	require.Empty(t, refreshSkill(context.Background(), clirender.NewTasks(&out), executable, home))
	recorded, err := os.ReadFile(argv)
	require.NoError(t, err)
	require.Equal(t, "skill install --agent claude\n", string(recorded))
	require.Contains(t, out.String(), "Refresh agent skill")
	require.Contains(t, out.String(), "Claude Code")

	failing := writeExecutable(t, t.TempDir(), "skali", []byte("#!/bin/sh\necho boom >&2\nexit 3\n"))
	warning := refreshSkill(context.Background(), clirender.NewTasks(&out), failing, home)
	require.Contains(t, warning, "refresh agent skill: ")
	require.Contains(t, warning, "boom")
	require.Contains(t, warning, "run skali skill install")
}
