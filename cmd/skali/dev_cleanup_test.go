package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/localdev"
	"github.com/stretchr/testify/require"
)

func TestFailedDevDeletionRetainsCredentialsForRetry(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("SKALI_DEV_CLUSTER", "skali-dev-cleanup-test")
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := writeExecutable(t, bin, "k3d", []byte("#!/bin/sh\nif [ \"$2\" = list ]; then echo '[{\"name\":\"skali-dev-cleanup-test\",\"nodes\":[{\"State\":{\"Running\":true}}]}]'; else echo interrupted >&2; exit 1; fi\n"))
	state, err := localdev.NewState("")
	require.NoError(t, err)
	require.NoError(t, localdev.SaveState(state))
	seedConfig(t, &cliconfig.Config{Remotes: map[string]*cliconfig.Remote{localRemoteName: {Master: localdev.MasterURL(), Token: "keep-token"}}})
	dir, err := localdev.ClusterDir(localdev.ClusterName())
	require.NoError(t, err)
	credentials := filepath.Join(dir, "registries.yaml")
	require.NoError(t, os.WriteFile(credentials, []byte("keep-password"), 0600))
	err = destroyLocalPlatform(context.Background(), io.Discard)
	require.ErrorContains(t, err, "interrupted")
	retained, err := localdev.LoadState()
	require.NoError(t, err)
	require.Equal(t, state.AuthSecret, retained.AuthSecret)
	require.Equal(t, "keep-token", loadConfig(t).Remotes[localRemoteName].Token)
	require.FileExists(t, credentials)
	// Cleanup may have removed the container before interruption. The retry
	// sees it absent and still removes its record and credentials safely.
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\necho '[]'\n"), 0755))
	require.NoError(t, destroyLocalPlatform(context.Background(), io.Discard))
	_, err = localdev.LoadState()
	require.ErrorIs(t, err, localdev.ErrNotInstalled)
	require.NoFileExists(t, credentials)
	require.NotContains(t, loadConfig(t).Remotes, localRemoteName)
}

func TestDevStatusDoesNotInventAWorkingTreeInstallation(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	bin := t.TempDir()
	writeExecutable(t, bin, "k3d", []byte("#!/bin/sh\necho '[]'\n"))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := runCapturingStdout(t, func() error { return execute(newRootCommand(), "dev", "status") })
	require.NoError(t, err)
	require.Contains(t, out, "no installation record")
	require.NotContains(t, out, "working tree")
}
