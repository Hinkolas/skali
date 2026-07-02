// Package cliconfig reads and writes the skali CLI's client-side
// configuration: kubectl-style named contexts, each holding a master URL and
// the session token minted for it. The file lives under XDG config
// (~/.config/skali/config.yaml) with 0600 permissions — it stores bearer
// tokens, nothing else does.
package cliconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Context is one master a user can talk to.
type Context struct {
	Master string `yaml:"master"`
	Token  string `yaml:"token,omitempty"`
}

// Config is the on-disk shape of ~/.config/skali/config.yaml.
type Config struct {
	CurrentContext string              `yaml:"current_context,omitempty"`
	Contexts       map[string]*Context `yaml:"contexts,omitempty"`
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
		return &Config{Contexts: map[string]*Context{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cliconfig: read %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("cliconfig: parse %s: %w", path, err)
	}
	if cfg.Contexts == nil {
		cfg.Contexts = map[string]*Context{}
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

// Current returns the active context, or an error telling the user how to
// create one.
func (c *Config) Current() (string, *Context, error) {
	if c.CurrentContext == "" {
		return "", nil, errors.New("no context selected; run `skali auth login --master <url>` first")
	}
	ctx, ok := c.Contexts[c.CurrentContext]
	if !ok {
		return "", nil, fmt.Errorf("current context %q does not exist; run `skali context list`", c.CurrentContext)
	}
	return c.CurrentContext, ctx, nil
}
