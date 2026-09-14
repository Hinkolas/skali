package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/stretchr/testify/require"
)

func TestRemoteCommandsCanListAndRemoveInvalidEntries(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	path, err := cliconfig.Path()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	require.NoError(t, os.WriteFile(path, []byte("current_remote: broken\nremotes:\n  broken: {token: secret}\n  null-entry: null\n  valid: {master: https://valid}\n"), 0600))
	var out bytes.Buffer
	root := newRootCommand()
	root.SetOut(&out)
	require.NoError(t, execute(root, "remote", "list"))
	require.Contains(t, out.String(), "broken (current)")
	require.Contains(t, out.String(), "invalid entry")
	require.Contains(t, out.String(), "null-entry")
	require.Contains(t, out.String(), path)
	for _, name := range []string{"broken", "null-entry"} {
		root := newRootCommand()
		root.SetOut(&out)
		require.NoError(t, execute(root, "remote", "remove", name))
	}
	cfg, err := cliconfig.Load()
	require.NoError(t, err)
	require.Len(t, cfg.Remotes, 1)
	require.Empty(t, cfg.CurrentRemote)
}
