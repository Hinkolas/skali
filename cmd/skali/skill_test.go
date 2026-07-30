package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/skill"
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
