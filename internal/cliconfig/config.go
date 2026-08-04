// Package cliconfig reads and writes the skali CLI's client-side
// configuration: named remotes, each holding a master URL and the session
// token minted for it. The file lives under XDG config
// (~/.config/skali/config.yaml) with 0600 permissions; it stores bearer
// tokens, nothing else does.
package cliconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Remote is one master a user can talk to. Instance pins the installation
// identity the master answered with when the remote was added (trust on
// first use), so a later reinstall of the cluster is detected instead of
// surfacing as a confusing expired session; empty until observed.
type Remote struct {
	Master   string `yaml:"master"`
	Token    string `yaml:"token,omitempty"`
	Instance string `yaml:"instance,omitempty"`
}

// Config is the on-disk shape of ~/.config/skali/config.yaml.
type Config struct {
	CurrentRemote string             `yaml:"current_remote,omitempty"`
	Remotes       map[string]*Remote `yaml:"remotes,omitempty"`
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
	path, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Config{Remotes: map[string]*Remote{}}, nil
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
	return &cfg, nil
}

// Save writes the config with token-safe permissions (dir 0700, file 0600).
func Save(cfg *Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("cliconfig: mkdir: %w", err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("cliconfig: marshal: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("cliconfig: write %s: %w", path, err)
	}
	return nil
}

// Current returns the active remote, or an error telling the user how to
// create one.
func (c *Config) Current() (string, *Remote, error) {
	if c.CurrentRemote == "" {
		return "", nil, errors.New("no remote selected; run `skali remote add <url>` first")
	}
	remote, ok := c.Remotes[c.CurrentRemote]
	if !ok {
		return "", nil, fmt.Errorf("current remote %q does not exist; run `skali remote list`", c.CurrentRemote)
	}
	return c.CurrentRemote, remote, nil
}
