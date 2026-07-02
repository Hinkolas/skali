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
	require.Empty(t, cfg.CurrentContext)
	require.NotNil(t, cfg.Contexts)

	_, _, err = cfg.Current()
	require.Error(t, err)
}

func TestSaveLoadRoundTripAndPermissions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	cfg := &Config{
		CurrentContext: "home",
		Contexts: map[string]*Context{
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

	name, ctx, err := got.Current()
	require.NoError(t, err)
	require.Equal(t, "home", name)
	require.Equal(t, "secret-token", ctx.Token)
}
