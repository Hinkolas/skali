package cliconfig

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRepairIncompleteEntriesPreservesOtherConfiguration(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := Path()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	require.NoError(t, os.WriteFile(path, []byte("future: {enabled: true}\ncurrent_remote: broken\nremotes:\n  broken: {token: secret}\n  null-entry: null\n  valid: {master: https://valid, future: keep}\n"), 0600))
	_, err = Load()
	require.ErrorContains(t, err, path)
	cfg, err := LoadForRepair()
	require.NoError(t, err)
	require.NoError(t, Remove("broken", cfg.Remotes["broken"]))
	require.NoError(t, Remove("null-entry", nil))
	cfg, err = Load()
	require.NoError(t, err)
	require.Empty(t, cfg.CurrentRemote)
	require.Len(t, cfg.Remotes, 1)
	require.Equal(t, "keep", cfg.Remotes["valid"].Extra["future"])
	require.Contains(t, cfg.Extra, "future")
}

func TestRepairRemovalProtectsChangedLoginAndNewSelection(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	require.NoError(t, Save(&Config{CurrentRemote: "main", Remotes: map[string]*Remote{"main": {Master: "https://main", Token: "old"}, "other": {Master: "https://other"}}}))
	old, err := Load()
	require.NoError(t, err)
	require.NoError(t, Update(func(c *Config) error { c.Remotes["main"].Token = "new"; return nil }))
	require.ErrorContains(t, Remove("main", old.Remotes["main"]), "changed concurrently")
	current, err := Load()
	require.NoError(t, err)
	require.NoError(t, Update(func(c *Config) error { c.CurrentRemote = "other"; c.Remotes["main"].Version = "v0.4.0"; return nil }))
	require.NoError(t, Remove("main", current.Remotes["main"]))
	current, err = Load()
	require.NoError(t, err)
	require.Equal(t, "other", current.CurrentRemote)
	require.NotContains(t, current.Remotes, "main")
}

func TestRepairNeverRewritesUnparseableConfiguration(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := Path()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	raw := []byte("remotes: [")
	require.NoError(t, os.WriteFile(path, raw, 0600))
	require.ErrorContains(t, Remove("broken", nil), path)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, raw, after)
}
