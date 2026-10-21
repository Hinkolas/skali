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

// writeFakeDocker answers the remnant sweep of a reset: listing is the
// shell output for every docker listing (containers, volumes and networks
// alike), every call is logged to the returned file, and a call starting
// with fail exits 1.
func writeFakeDocker(t *testing.T, bin, listing, fail string) string {
	t.Helper()
	calls := filepath.Join(bin, "docker-calls")
	script := "#!/bin/sh\necho \"$*\" >> '" + calls + "'\n"
	if fail != "" {
		script += "case \"$*\" in '" + fail + "'*) echo 'removal refused' >&2; exit 1;; esac\n"
	}
	script += "case \"$*\" in *' ls '*|ps*) " + listing + ";; esac\n"
	writeExecutable(t, bin, "docker", []byte(script))
	return calls
}

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
	writeFakeDocker(t, bin, "echo 3f2a9c", "rm")
	retained := func() {
		t.Helper()
		record, err := localdev.LoadState()
		require.NoError(t, err)
		require.Equal(t, state.AuthSecret, record.AuthSecret)
		require.Equal(t, "keep-token", loadConfig(t).Remotes[localRemoteName].Token)
		require.FileExists(t, credentials)
	}
	err = destroyLocalPlatform(context.Background(), io.Discard)
	require.ErrorContains(t, err, "interrupted")
	retained()
	// Cleanup may have removed the container before interruption. The retry
	// no longer sees the cluster, but the remnant sweep still runs, and its
	// failure keeps the record and credentials as well.
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\necho '[]'\n"), 0755))
	err = destroyLocalPlatform(context.Background(), io.Discard)
	require.ErrorContains(t, err, "docker rm --force --volumes 3f2a9c")
	retained()
	// Once the remnants are gone, the record and credentials go too.
	writeFakeDocker(t, bin, "true", "")
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

func TestExplicitDevResetDeletesUnrecordedFixedCluster(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("SKALI_DEV_CLUSTER", "skali-dev-recovery-test")
	configPath, err := cliconfig.Path()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(configPath), 0700))
	require.NoError(t, os.WriteFile(configPath, []byte("remotes:\n  local: null\n  broken: {token: keep}\n"), 0600))
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	calls := filepath.Join(bin, "deleted")
	t.Setenv("SKALI_TEST_DELETED", calls)
	writeExecutable(t, bin, "k3d", []byte("#!/bin/sh\nif [ \"$2\" = list ]; then echo '[{\"name\":\"skali-dev-recovery-test\",\"nodes\":[{\"State\":{\"Running\":true}}]}]'; elif [ \"$2\" = delete ]; then echo \"$3\" > \"$SKALI_TEST_DELETED\"; else exit 1; fi\n"))
	writeFakeDocker(t, bin, "true", "")
	_, err = runCapturingStdout(t, func() error { return execute(newRootCommand(), "dev", "reset", "--yes") })
	require.NoError(t, err)
	deleted, err := os.ReadFile(calls)
	require.NoError(t, err)
	require.Equal(t, "skali-dev-recovery-test\n", string(deleted))
	_, err = localdev.LoadState()
	require.ErrorIs(t, err, localdev.ErrNotInstalled)
	cfg, err := cliconfig.LoadForRepair()
	require.NoError(t, err)
	require.NotContains(t, cfg.Remotes, localRemoteName)
	require.Equal(t, "keep", cfg.Remotes["broken"].Token)
}

// A partial cluster, here only the k3d tools helper and the network,
// hides from k3d cluster list but holds the name k3d cluster create needs.
// Reset removes it along with the record instead of trusting the listing
// (#232).
func TestDevResetRemovesUnlistedPartialCluster(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("SKALI_DEV_CLUSTER", "skali-dev-partial-test")
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeExecutable(t, bin, "k3d", []byte("#!/bin/sh\nif [ \"$2\" = list ]; then echo '[]'; fi\n"))
	calls := writeFakeDocker(t, bin, `case "$*" in ps*) echo 3f2a9c;; network*) printf 'bridge\nk3d-skali-dev-partial-test\nk3d-skali-dev\n';; esac`, "")
	state, err := localdev.NewState("")
	require.NoError(t, err)
	require.NoError(t, localdev.SaveState(state))
	seedConfig(t, &cliconfig.Config{Remotes: map[string]*cliconfig.Remote{localRemoteName: {Master: localdev.MasterURL(), Token: "stale"}}})

	out, err := runCapturingStdout(t, func() error { return execute(newRootCommand(), "dev", "reset", "--yes") })
	require.NoError(t, err)
	require.Contains(t, out, "Delete cluster skali-dev-partial-test")
	docker, err := os.ReadFile(calls)
	require.NoError(t, err)
	require.Contains(t, string(docker), "rm --force --volumes 3f2a9c\n")
	require.Contains(t, string(docker), "network rm k3d-skali-dev-partial-test\n")
	require.NotContains(t, string(docker), "network rm k3d-skali-dev\n")
	_, err = localdev.LoadState()
	require.ErrorIs(t, err, localdev.ErrNotInstalled)
	require.NotContains(t, loadConfig(t).Remotes, localRemoteName)
}

func TestDevStopReportsObservedState(t *testing.T) {
	for _, tc := range []struct {
		name, listing, message string
		stop                   bool
	}{
		{"absent", "[]", "cluster is absent", false},
		{"stopped", `[{"name":"skali-dev-stop-test","nodes":[{"State":{"Running":false}}]}]`, "already stopped", false},
		{"running", `[{"name":"skali-dev-stop-test","nodes":[{"State":{"Running":true}}]}]`, "stopped local platform", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			t.Setenv("SKALI_DEV_CLUSTER", "skali-dev-stop-test")
			bin := t.TempDir()
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			calls := filepath.Join(bin, "stopped")
			t.Setenv("SKALI_TEST_STOPPED", calls)
			writeExecutable(t, bin, "k3d", []byte("#!/bin/sh\nif [ \"$2\" = list ]; then echo '"+tc.listing+"'; elif [ \"$2\" = stop ]; then touch \"$SKALI_TEST_STOPPED\"; else exit 1; fi\n"))
			state, err := localdev.NewState("")
			require.NoError(t, err)
			require.NoError(t, localdev.SaveState(state))
			out, err := runCapturingStdout(t, func() error { return execute(newRootCommand(), "dev", "stop") })
			require.NoError(t, err)
			require.Contains(t, out, tc.message)
			if tc.stop {
				require.FileExists(t, calls)
			} else {
				require.NoFileExists(t, calls)
				require.NotContains(t, out, "stopped local platform")
			}
			retained, err := localdev.LoadState()
			require.NoError(t, err)
			require.Equal(t, state.AuthSecret, retained.AuthSecret)
		})
	}
}

func TestWorkingTreeDevRejectsReleasedImageBeforePlatformChanges(t *testing.T) {
	withCLIVersion(t, "v0.0.0-dev")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	command := newRootCommand()
	command.SetContext(context.Background())
	_, err := ensureLocalPlatform(command, "ghcr.io/hinkolas/skalid:v0.1.0-rc.3", false)
	require.ErrorContains(t, err, "requires a development image")
	_, err = localdev.LoadState()
	require.ErrorIs(t, err, localdev.ErrNotInstalled)
}
