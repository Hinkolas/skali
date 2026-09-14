// Package skill embeds the skali skill for coding agents (the SKILL.md
// format shared by Claude Code and Codex). The content comes in two sets
// (docs/versioning.md, decision 6): the installed set, a version-neutral
// SKILL.md shell plus the architecture guide, written into user-level
// agent skill directories; and the references, version-bound documents
// (the manifest grammar, the CLI surface) served by skali skill read,
// which dispatches to the release of the project's target cluster. The
// installed directory is installer-owned: install replaces it wholesale,
// so local edits do not survive a reinstall.
package skill

import (
	"context"
	"embed"
	"fmt"
	"github.com/Hinkolas/skali/internal/filelock"
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
// user-owned and never replaced. The string is stable across releases so
// installs from older ones are recognized.
const managedMarker = `Managed by "skali skill install"`

// FS returns the installed set with SKILL.md at the root.
func FS() fs.FS {
	sub, err := fs.Sub(assets, "assets/skill")
	if err != nil {
		panic(err)
	}
	return sub
}

// Topic is one reference skali skill read serves.
type Topic struct {
	// Name is the argument to skali skill read.
	Name string
	// Summary is the one-line description in the topic list.
	Summary string
}

// Topics lists the references in display order.
func Topics() []Topic {
	return []Topic{
		{Name: "manifest", Summary: "every skali.yaml field, default, unit, and validation rule"},
		{Name: "cli", Summary: "the skali commands an agent uses from the terminal"},
		{Name: "architecture", Summary: "application architecture, defaults, and platform lifecycle"},
	}
}

// Reference returns the embedded reference for a topic; ok is false for
// a name Topics does not list.
func Reference(name string) (content []byte, ok bool) {
	for _, topic := range Topics() {
		if topic.Name == name {
			data, err := assets.ReadFile("assets/reference/" + name + ".md")
			if err != nil {
				panic(err)
			}
			return data, true
		}
	}
	return nil, false
}

// TopicNames lists the topic names in display order.
func TopicNames() []string {
	topics := Topics()
	names := make([]string, 0, len(topics))
	for _, topic := range topics {
		names = append(names, topic.Name)
	}
	return names
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

// Installed reports whether the agent's skill directory under home holds
// a SKILL.md written by this installer. Upgrades refresh exactly those.
func Installed(home string, agent Agent) bool {
	existing, err := os.ReadFile(filepath.Join(agent.Dir(home), "SKILL.md"))
	return err == nil && strings.Contains(string(existing), managedMarker)
}

// Install replaces the agent's skali skill directory under home with the
// installed set and returns the written paths, sorted. The directory is
// installer-owned, so files from older releases are pruned; a SKILL.md
// that was not written by this installer is refused rather than replaced.
// CLAUDE_CONFIG_DIR-style overrides are not honored yet.
func Install(home string, agent Agent) ([]string, error) {
	dir := agent.Dir(home)
	unlock, err := filelock.Acquire(context.Background(), dir+".lock")
	if err != nil {
		return nil, err
	}
	defer unlock()
	existing, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	switch {
	case err == nil:
		if !strings.Contains(string(existing), managedMarker) {
			return nil, fmt.Errorf("%s exists but was not installed by skali; remove the directory to let install replace it", dir)
		}
	case !os.IsNotExist(err):
		return nil, fmt.Errorf("inspect %s: %w", dir, err)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
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
		if err := writeAtomic(target, data); err != nil {
			return err
		}
		written = append(written, target)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("write skill to %s: %w", dir, err)
	}
	// Publish the new shell before pruning stale references. Readers always
	// see a complete SKILL.md, and an interrupted install is safe to retry.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	keep := map[string]bool{}
	for _, path := range written {
		keep[path] = true
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		if !keep[path] {
			if err := os.RemoveAll(path); err != nil {
				return nil, err
			}
		}
	}
	sort.Strings(written)
	return written, nil
}

func writeAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".skill-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(0644); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
