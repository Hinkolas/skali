// Package skill embeds the skali skill for coding agents (the SKILL.md
// format shared by Claude Code and Codex) and installs it into user-level
// agent skill directories. The installed directory is installer-owned:
// install replaces it wholesale, so local edits do not survive a reinstall.
package skill

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed all:assets
var assets embed.FS

// managedMarker identifies a skill directory written by this installer.
// SKILL.md carries it; an existing SKILL.md without it is treated as
// user-owned and never replaced.
const managedMarker = `Managed by "skali skill install"`

// FS returns the skill content with SKILL.md at the root.
func FS() fs.FS {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		panic(err)
	}
	return sub
}

// Agent is a coding agent the skill can be installed for.
type Agent struct {
	// Name is the stable flag and prompt value, such as "claude".
	Name string
	// Label is the human-facing name, such as "Claude Code".
	Label string
	// Dir returns the user-level skill directory under the given home.
	Dir func(home string) string
}

// Agents lists the supported agents in prompt order.
func Agents() []Agent {
	return []Agent{
		{
			Name:  "claude",
			Label: "Claude Code",
			Dir: func(home string) string {
				return filepath.Join(home, ".claude", "skills", "skali")
			},
		},
		{
			Name:  "codex",
			Label: "Codex",
			Dir: func(home string) string {
				return filepath.Join(home, ".agents", "skills", "skali")
			},
		},
	}
}

// AgentByName resolves a flag value to a supported agent.
func AgentByName(name string) (Agent, error) {
	agents := Agents()
	for _, agent := range agents {
		if agent.Name == name {
			return agent, nil
		}
	}
	names := make([]string, 0, len(agents))
	for _, agent := range agents {
		names = append(names, agent.Name)
	}
	return Agent{}, fmt.Errorf("unknown agent %q (valid: %s)", name, strings.Join(names, ", "))
}

// Install replaces the agent's skali skill directory under home with the
// embedded content and returns the written paths, sorted. The directory is
// installer-owned, so files from older releases are pruned; a SKILL.md
// that was not written by this installer is refused rather than replaced.
// CLAUDE_CONFIG_DIR-style overrides are not honored yet.
func Install(home string, agent Agent) ([]string, error) {
	dir := agent.Dir(home)
	existing, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	switch {
	case err == nil:
		if !strings.Contains(string(existing), managedMarker) {
			return nil, fmt.Errorf("%s exists but was not installed by skali; remove the directory to let install replace it", dir)
		}
	case !os.IsNotExist(err):
		return nil, fmt.Errorf("inspect %s: %w", dir, err)
	}
	if err := os.RemoveAll(dir); err != nil {
		return nil, fmt.Errorf("clear %s: %w", dir, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}

	var written []string
	content := FS()
	err = fs.WalkDir(content, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.FromSlash(path))
		if entry.IsDir() {
			if path == "." {
				return nil
			}
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(content, path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return err
		}
		written = append(written, target)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("write skill to %s: %w", dir, err)
	}
	sort.Strings(written)
	return written, nil
}
