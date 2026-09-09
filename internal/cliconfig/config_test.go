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

func TestSaveRepairsExistingPermissions(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := Path()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("old"), 0o644))
	require.NoError(t, os.Chmod(path, 0o644))
	require.NoError(t, os.Chmod(filepath.Dir(path), 0o755))
	require.NoError(t, Save(&Config{}))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	info, err = os.Stat(filepath.Dir(path))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

func TestSaveReplacesSymlinkWithoutWritingTarget(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path, err := Path()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	target := filepath.Join(dir, "target")
	require.NoError(t, os.WriteFile(target, []byte("untouched"), 0o644))
	require.NoError(t, os.Symlink(target, path))
	require.NoError(t, Save(&Config{}))
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "untouched", string(data))
	info, err := os.Lstat(path)
	require.NoError(t, err)
	require.True(t, info.Mode().IsRegular())
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
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

// TestLoadClearsLocalCurrentRemote covers the lazy migration from CLIs
// where skali dev made the local platform the current remote: the selection
// is dropped on load so nothing targets the local platform implicitly.
func TestLoadClearsLocalCurrentRemote(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	require.NoError(t, Save(&Config{
		CurrentRemote: LocalRemoteName,
		Remotes: map[string]*Remote{
			LocalRemoteName: {Master: "http://skali.localhost:8080", Token: "tok"},
			"prod":          {Master: "https://skali.example.com"},
		},
	}))

	cfg, err := Load()
	require.NoError(t, err)
	require.Empty(t, cfg.CurrentRemote)
	require.NotNil(t, cfg.Remotes[LocalRemoteName])

	// With a selectable remote on file the error points at `use`, and the
	// local remote alone does not.
	_, _, err = cfg.Current()
	require.ErrorContains(t, err, "skali remote use")
	delete(cfg.Remotes, "prod")
	_, _, err = cfg.Current()
	require.ErrorContains(t, err, "skali remote add")
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
