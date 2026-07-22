package cliconfig

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadMissingFileIsEmptyConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cfg, err := Load()
	require.NoError(t, err)
	require.Empty(t, cfg.CurrentRemote)
	require.NotNil(t, cfg.Remotes)

	_, _, err = cfg.Current()
	require.Error(t, err)
}

func TestSaveLoadRoundTripAndPermissions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	cfg := &Config{
		CurrentRemote: "home",
		Remotes: map[string]*Remote{
			"home": {Master: "http://localhost:7070", Token: "secret-token"},
			"prod": {Master: "https://skali.example.com"},
		},
	}
	require.NoError(t, Save(cfg))

	// Token-safe permissions: file 0600, dir 0700.
	path := filepath.Join(dir, "skali", "config.yaml")
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	dirInfo, err := os.Stat(filepath.Dir(path))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())

	got, err := Load()
	require.NoError(t, err)
	require.Equal(t, cfg, got)

	name, remote, err := got.Current()
	require.NoError(t, err)
	require.Equal(t, "home", name)
	require.Equal(t, "secret-token", remote.Token)
}

// TestOldContextKeysAreIgnored documents the deliberate no-migration decision:
// a pre-rename config file (contexts:/current_context:) loads as empty, and
// the first Save after a remote add rewrites it with the new keys only.
func TestOldContextKeysAreIgnored(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	path := filepath.Join(dir, "skali", "config.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	old := "current_context: khz\ncontexts:\n  khz:\n    master: https://skali.example.com\n    token: stale-token\n"
	require.NoError(t, os.WriteFile(path, []byte(old), 0o600))

	cfg, err := Load()
	require.NoError(t, err)
	require.Empty(t, cfg.CurrentRemote)
	require.Empty(t, cfg.Remotes)

	_, _, err = cfg.Current()
	require.ErrorContains(t, err, "no remote selected")
}
