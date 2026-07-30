package skill_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Hinkolas/skali/internal/skill"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestInstallWritesSkillForEachAgent(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	for _, agent := range skill.Agents() {
		paths, err := skill.Install(home, agent)
		require.NoError(t, err)
		require.NotEmpty(t, paths)
		require.True(t, sort.StringsAreSorted(paths))

		manifest := filepath.Join(agent.Dir(home), "SKILL.md")
		require.Contains(t, paths, manifest)
		embedded, err := fs.ReadFile(skill.FS(), "SKILL.md")
		require.NoError(t, err)
		installed, err := os.ReadFile(manifest)
		require.NoError(t, err)
		require.Equal(t, embedded, installed)
	}

	require.DirExists(t, filepath.Join(home, ".claude", "skills", "skali"))
	require.DirExists(t, filepath.Join(home, ".agents", "skills", "skali"))
}

func TestInstallPrunesStaleFiles(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	agent := skill.Agents()[0]
	_, err := skill.Install(home, agent)
	require.NoError(t, err)

	dir := agent.Dir(home)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "stale.md"), []byte("old"), 0o644))
	embedded, err := fs.ReadFile(skill.FS(), "SKILL.md")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), append(embedded, "\nlocal edit\n"...), 0o644))

	_, err = skill.Install(home, agent)
	require.NoError(t, err)
	require.NoFileExists(t, filepath.Join(dir, "stale.md"))
	installed, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	require.NoError(t, err)
	require.Equal(t, embedded, installed)
}

func TestInstallRefusesUnmanagedSkill(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	agent := skill.Agents()[0]
	dir := agent.Dir(home)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: skali\n---\nmy own skill\n"), 0o644))

	_, err := skill.Install(home, agent)
	require.ErrorContains(t, err, "was not installed by skali")
	installed, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	require.NoError(t, err)
	require.Contains(t, string(installed), "my own skill")
}

func TestSkillFrontmatter(t *testing.T) {
	t.Parallel()

	data, err := fs.ReadFile(skill.FS(), "SKILL.md")
	require.NoError(t, err)
	text := string(data)
	require.True(t, strings.HasPrefix(text, "---\n"))
	end := strings.Index(text[4:], "\n---\n")
	require.Positive(t, end, "frontmatter closing delimiter missing")

	var meta struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(text[4:4+end]), &meta))
	require.Equal(t, "skali", meta.Name)
	require.NotEmpty(t, meta.Description)
	require.LessOrEqual(t, len(meta.Description), 1024)
}

func TestAgentByName(t *testing.T) {
	t.Parallel()

	for _, agent := range skill.Agents() {
		resolved, err := skill.AgentByName(agent.Name)
		require.NoError(t, err)
		require.Equal(t, agent.Label, resolved.Label)
	}
	_, err := skill.AgentByName("cursor")
	require.ErrorContains(t, err, `unknown agent "cursor"`)
	require.ErrorContains(t, err, "claude, codex")
}
