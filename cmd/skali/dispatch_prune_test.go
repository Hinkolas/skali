package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/installer"
)

func TestPruneCLICache(t *testing.T) {
	cacheDir := t.TempDir()
	seed := func(release string, age time.Duration) string {
		path := installer.CLICachePath(cacheDir, release)
		require.NoError(t, installer.StoreBinary(path, fakeCLI(release), "00"))
		stamp := time.Now().Add(-age)
		require.NoError(t, os.Chtimes(filepath.Dir(path), stamp, stamp))
		return filepath.Dir(path)
	}
	old := seed("v0.2.0", time.Hour)
	referenced := seed("v0.4.0", time.Hour)
	home := seed("v0.3.2", time.Hour)
	fresh := seed("v0.1.0", time.Minute)
	trash := filepath.Join(installer.CLICacheDir(cacheDir), ".trash-v0.0.9-1")
	require.NoError(t, os.MkdirAll(trash, 0o755))
	notes := filepath.Join(installer.CLICacheDir(cacheDir), "notes.txt")
	require.NoError(t, os.WriteFile(notes, []byte("keep"), 0o644))
	cfg := &cliconfig.Config{Remotes: map[string]*cliconfig.Remote{
		"khz": {Master: "https://khz.example/api", Version: "v0.4.0"},
		"dev": {Master: "https://dev.example/api", Version: "v0.0.0-dev"},
	}}

	pruneCLICache(cfg, "v0.3.2", cacheDir)

	_, err := os.Stat(old)
	require.ErrorIs(t, err, os.ErrNotExist, "unreferenced release removed")
	_, err = os.Stat(trash)
	require.ErrorIs(t, err, os.ErrNotExist, "stale trash swept")
	for _, kept := range []string{referenced, home, fresh, notes} {
		_, err := os.Stat(kept)
		require.NoError(t, err, "%s kept", kept)
	}

	// A missing cache is fine, and so is a nil config.
	pruneCLICache(nil, "v0.3.2", filepath.Join(t.TempDir(), "absent"))
	pruneCLICache(nil, "v0.3.2", cacheDir)
	_, err = os.Stat(referenced)
	require.ErrorIs(t, err, os.ErrNotExist, "without a config only home is referenced")
	_, err = os.Stat(home)
	require.NoError(t, err)
}
