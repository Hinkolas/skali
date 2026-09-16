// Package cliconfig reads and writes the skali CLI's client-side
// configuration: named remotes, each holding a master URL and the session
// token minted for it. The file lives under XDG config
// (~/.config/skali/config.yaml) with 0600 permissions; it stores bearer
// tokens, nothing else does.
package cliconfig

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/Hinkolas/skali/internal/filelock"

	"gopkg.in/yaml.v3"
)

// LocalRemoteName is the reserved remote name of the dev-owned local
// platform. skali dev creates and refreshes it; it is hidden from remote
// listings and never becomes the current remote, so generic commands
// cannot target the local platform by accident.
const LocalRemoteName = "local"

// Remote is one master a user can talk to. Instance pins the installation
// identity the master answered with when the remote was added (trust on
// first use), so a later reinstall of the cluster is detected instead of
// surfacing as a confusing expired session; empty until observed. Version
// is the daemon build the master last answered with: dispatch runs the
// skali release matching it (docs/versioning.md, decision 1). Both are
// additive fields older binaries ignore.
type Remote struct {
	Master   string         `yaml:"master"`
	Token    string         `yaml:"token,omitempty"`
	Instance string         `yaml:"instance,omitempty"`
	Version  string         `yaml:"version,omitempty"`
	Extra    map[string]any `yaml:",inline"`
}

// Config is the on-disk shape of ~/.config/skali/config.yaml.
type Config struct {
	CurrentRemote string             `yaml:"current_remote,omitempty"`
	Remotes       map[string]*Remote `yaml:"remotes,omitempty"`
	Extra         map[string]any     `yaml:",inline"`
	loaded        *Config
}

// Path resolves the config file location: $XDG_CONFIG_HOME/skali/config.yaml,
// defaulting XDG_CONFIG_HOME to ~/.config (explicitly, not os.UserConfigDir,
// so the location is the same on every OS).
func Path() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cliconfig: resolve home: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "skali", "config.yaml"), nil
}

// Load reads the config; a missing file is an empty config, not an error.
func Load() (*Config, error) {
	cfg, err := LoadForRepair()
	if err != nil {
		return nil, err
	}
	for name, remote := range cfg.Remotes {
		if remote == nil || remote.Master == "" {
			path, _ := Path()
			return nil, fmt.Errorf("cliconfig: %s: remote %q has no master; remove it with skali remote remove %s or edit the file", path, name, name)
		}
	}
	return cfg, nil
}

// LoadForRepair parses configuration without requiring usable remote entries.
// Inspection and removal remain available when an entry is incomplete.
func LoadForRepair() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Config{Remotes: map[string]*Remote{}, loaded: &Config{Remotes: map[string]*Remote{}}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cliconfig: read %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("cliconfig: parse %s: %w", path, err)
	}
	if cfg.Remotes == nil {
		cfg.Remotes = map[string]*Remote{}
	}
	// Older CLIs made the local platform the current remote; treat that as
	// no selection so unbound deploys block instead of silently targeting
	// it. The cleared value persists on the next save.
	if cfg.CurrentRemote == LocalRemoteName {
		cfg.CurrentRemote = ""
	}
	cfg.loaded = clone(&cfg)
	return &cfg, nil
}

func clone(cfg *Config) *Config {
	data, _ := yaml.Marshal(cfg)
	var copy Config
	_ = yaml.Unmarshal(data, &copy)
	if copy.Remotes == nil {
		copy.Remotes = map[string]*Remote{}
	}
	return &copy
}

// Update applies a narrow mutation to the latest configuration while locked.
// Unknown fields survive both reading and writing, including inside remotes.
func Update(fn func(*Config) error) error {
	return update(Load, fn)
}

func update(load func() (*Config, error), fn func(*Config) error) error {
	path, err := Path()
	if err != nil {
		return err
	}
	unlock, err := filelock.Acquire(context.Background(), path+".lock")
	if err != nil {
		return err
	}
	defer unlock()
	cfg, err := load()
	if err != nil {
		return err
	}
	if err := fn(cfg); err != nil {
		return err
	}
	return write(cfg)
}

// Remove conditionally removes an entry, including an incomplete or null entry.
// Parse errors still fail without changing the source file.
func Remove(name string, expected *Remote) error {
	return update(LoadForRepair, func(cfg *Config) error {
		current, exists := cfg.Remotes[name]
		if !exists {
			return nil
		}
		if (current == nil) != (expected == nil) || current != nil && (current.Master != expected.Master || current.Token != expected.Token || expected.Instance != "" && current.Instance != expected.Instance) {
			return fmt.Errorf("remote %q changed concurrently; run the command again", name)
		}
		delete(cfg.Remotes, name)
		if cfg.CurrentRemote == name {
			cfg.CurrentRemote = ""
		}
		return nil
	})
}

// Save merges changes relative to the snapshot Load returned. Conflicting
// edits fail rather than overwriting another login or resurrecting a remote.
func Save(cfg *Config) error {
	err := Update(func(latest *Config) error {
		before := cfg.loaded
		if before == nil {
			before = &Config{Remotes: map[string]*Remote{}}
		}
		if cfg.CurrentRemote != before.CurrentRemote {
			// Explicit selections apply in lock order. A stale removal may only clear
			// the selection it read, never a newer selection made in the meantime.
			if cfg.CurrentRemote != "" || latest.CurrentRemote == before.CurrentRemote {
				latest.CurrentRemote = cfg.CurrentRemote
			}
		}

		for name, old := range before.Remotes {
			if _, exists := cfg.Remotes[name]; !exists {
				if current := latest.Remotes[name]; current != nil && (current.Master != old.Master || current.Token != old.Token || old.Instance != "" && current.Instance != old.Instance) {
					return fmt.Errorf("remote %q changed concurrently; run the command again", name)
				}
				delete(latest.Remotes, name)
			}
		}
		for name, wanted := range cfg.Remotes {
			old := before.Remotes[name]
			if reflect.DeepEqual(old, wanted) {
				continue
			}
			current := latest.Remotes[name]
			if old == nil || current == nil {
				if !reflect.DeepEqual(current, old) && !reflect.DeepEqual(current, wanted) {
					return fmt.Errorf("remote %q changed concurrently; run the command again", name)
				}
				latest.Remotes[name] = wanted
				continue
			}
			// Apply only fields this caller changed. Version observations may land
			// during a login without erasing it or rejecting an unrelated update.
			if current.Master != old.Master || current.Token != old.Token || (current.Instance != old.Instance && old.Instance != "") {
				return fmt.Errorf("remote %q changed concurrently; run the command again", name)
			}
			if wanted.Master != old.Master {
				current.Master = wanted.Master
			}
			if wanted.Token != old.Token {
				current.Token = wanted.Token
			}
			if wanted.Instance != old.Instance {
				current.Instance = wanted.Instance
			}
			if wanted.Version != old.Version && (current.Version == old.Version || wanted.Instance != old.Instance || wanted.Master != old.Master) {
				current.Version = wanted.Version
			}

		}
		return nil
	})
	if err == nil {
		cfg.loaded = clone(cfg)
	}
	return err
}

// Observe records platform headers only if this is still the same login and
// installation. A slow response cannot overwrite a new login or removed remote.
func Observe(name string, expected Remote, instance, version string) error {
	return Update(func(cfg *Config) error {
		current := cfg.Remotes[name]
		if current == nil || current.Master != expected.Master || current.Token != expected.Token || (current.Instance != expected.Instance && !(expected.Instance == "" && current.Instance == instance)) {
			return nil
		}
		if instance != "" {
			current.Instance = instance
		}
		if version != "" {
			current.Version = version
		}
		return nil
	})
}

func write(cfg *Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("cliconfig: mkdir: %w", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("cliconfig: protect directory: %w", err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("cliconfig: marshal: %w", err)
	}
	// A new, private inode both repairs existing loose permissions and avoids
	// following a config-file symlink. Rename leaves the previous file intact
	// if writing fails, rather than truncating a working login configuration.
	f, err := os.CreateTemp(filepath.Dir(path), ".config-*")
	if err != nil {
		return fmt.Errorf("cliconfig: create temporary file: %w", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("cliconfig: write %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("cliconfig: sync: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("cliconfig: close: %w", err)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("cliconfig: replace %s: %w", path, err)
	}
	return nil
}

// Current returns the active remote, or an error telling the user how to
// select or create one. The dev-owned local remote never counts: it is not
// selectable, so it must not turn the "add a remote" hint into "use one".
func (c *Config) Current() (string, *Remote, error) {
	if c.CurrentRemote == "" {
		for name := range c.Remotes {
			if name != LocalRemoteName {
				return "", nil, errors.New("no remote selected; run `skali remote use <name>`")
			}
		}
		return "", nil, errors.New("no remote selected; run `skali remote add <name> <url>` first")
	}
	remote, ok := c.Remotes[c.CurrentRemote]
	if !ok {
		return "", nil, fmt.Errorf("current remote %q does not exist; run `skali remote list`", c.CurrentRemote)
	}
	return c.CurrentRemote, remote, nil
}
