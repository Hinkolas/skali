package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/localdev"
)

func TestReferencedReleases(t *testing.T) {
	t.Parallel()
	cfg := &cliconfig.Config{Remotes: map[string]*cliconfig.Remote{
		"khz":           {Version: "v0.4.0"},
		"lab":           {Version: ""},
		localRemoteName: {Version: "v0.3.0"},
	}}
	require.Equal(t, map[string]bool{"v0.5.0": true, "v0.4.0": true, "v0.3.0": true}, referencedReleases(cfg, "v0.5.0", true))
	require.Equal(t, map[string]bool{"v0.5.0": true, "v0.4.0": true}, referencedReleases(cfg, "v0.5.0", false),
		"the local remote only mirrors whichever platform last logged in")
	require.Equal(t, map[string]bool{"v0.4.0": true}, referencedReleases(cfg, "", false))
	require.Equal(t, map[string]bool{"v0.0.0-dev": true}, referencedReleases(nil, "v0.0.0-dev", false))
}

func TestPruneCandidates(t *testing.T) {
	t.Parallel()
	records := []localdev.Record{
		{Name: "skali-dev", Version: "v0.1.0-rc.2", Legacy: true},
		{Name: "skali-dev-v0-4-0", Version: "v0.4.0"},
		{Name: "skali-dev-v0-5-0", Version: "v0.5.0"},
		{Name: "skali-dev-working-tree"},
	}
	names := func(candidates []localdev.Record) []string {
		var out []string
		for _, c := range candidates {
			out = append(out, c.Name)
		}
		return out
	}
	// A released home keeps its own release and every remote's; the
	// working tree is unreachable from a released binary; legacy always.
	require.Equal(t, []string{"skali-dev", "skali-dev-v0-4-0", "skali-dev-working-tree"},
		names(pruneCandidates(records, map[string]bool{"v0.5.0": true}, "v0.5.0")))
	require.Equal(t, []string{"skali-dev", "skali-dev-working-tree"},
		names(pruneCandidates(records, map[string]bool{"v0.5.0": true, "v0.4.0": true}, "v0.5.0")))
	// A development build keeps the working tree.
	require.Equal(t, []string{"skali-dev", "skali-dev-v0-5-0"},
		names(pruneCandidates(records, map[string]bool{"v0.4.0": true}, "v0.0.0-dev")))
}

// writeDevRecord fabricates one platform record under the scoped state
// home, the way Ensure writes it before creating the cluster.
func writeDevRecord(t *testing.T, stateHome, name string, state localdev.State) {
	t.Helper()
	dir := filepath.Join(stateHome, "skali", "dev", name)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	raw, err := json.Marshal(state)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "state.json"), raw, 0o600))
}

func stubLocalPlatforms(t *testing.T, statuses map[string]localdev.ClusterStatus) *[]string {
	t.Helper()
	var deleted []string
	previousStatuses, previousDelete := localStatuses, deleteLocalCluster
	localStatuses = func(context.Context) (map[string]localdev.ClusterStatus, error) { return statuses, nil }
	deleteLocalCluster = func(_ context.Context, name string) error {
		deleted = append(deleted, name)
		return nil
	}
	t.Cleanup(func() { localStatuses, deleteLocalCluster = previousStatuses, previousDelete })
	return &deleted
}

func TestDevPruneNothing(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	withCLIVersion(t, "v0.5.0")
	seedConfig(t, &cliconfig.Config{Remotes: map[string]*cliconfig.Remote{
		"khz":           {Master: "https://khz.example", Version: "v0.4.0"},
		localRemoteName: {Master: "http://skali.localhost:8080", Version: "v0.4.0"},
	}})
	writeDevRecord(t, stateHome, "skali-dev-v0-4-0", localdev.State{Version: "v0.4.0", SkalidImage: "ghcr.io/hinkolas/skalid:v0.4.0"})
	writeDevRecord(t, stateHome, "skali-dev-v0-5-0", localdev.State{Version: "v0.5.0", SkalidImage: "ghcr.io/hinkolas/skalid:v0.5.0"})
	deleted := stubLocalPlatforms(t, map[string]localdev.ClusterStatus{"skali-dev-v0-4-0": localdev.ClusterRunning})

	out, err := runCapturingStdout(t, func() error { return execute(newRootCommand(), "dev", "prune") })
	require.NoError(t, err)
	require.Equal(t, "nothing to prune: every local platform is in use\n"+
		"  skali-dev-v0-4-0                 skalid v0.4.0                  running  remote khz\n"+
		"  skali-dev-v0-5-0                 skalid v0.5.0                  absent   this skali\n", out)
	require.Empty(t, *deleted)
}

func TestDevPruneConfirms(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	withCLIVersion(t, "v0.5.0")
	seedConfig(t, &cliconfig.Config{Remotes: map[string]*cliconfig.Remote{
		localRemoteName: {Master: "http://skali.localhost:8080", Token: "t", Version: "v0.4.0"},
	}})
	created := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	writeDevRecord(t, stateHome, "skali-dev-v0-4-0", localdev.State{Version: "v0.4.0", SkalidImage: "ghcr.io/hinkolas/skalid:v0.4.0", CreatedAt: created})
	writeDevRecord(t, stateHome, "skali-dev-v0-5-0", localdev.State{Version: "v0.5.0", SkalidImage: "ghcr.io/hinkolas/skalid:v0.5.0", CreatedAt: created})
	writeDevRecord(t, stateHome, "skali-dev-working-tree", localdev.State{SkalidImage: "skalid:dev", CreatedAt: created})
	legacy, err := json.Marshal(localdev.State{Cluster: "skali-dev", SkalidImage: "ghcr.io/hinkolas/skalid:v0.1.0-rc.2"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(stateHome, "skali", "localdev.json"), legacy, 0o600))
	deleted := stubLocalPlatforms(t, map[string]localdev.ClusterStatus{
		"skali-dev":        localdev.ClusterStopped,
		"skali-dev-v0-4-0": localdev.ClusterRunning,
	})

	// Declined by default: a bare newline is No, and nothing moves.
	out, err := runCapturingStdout(t, func() error {
		return withStdin(t, "\n", func() error { return execute(newRootCommand(), "dev", "prune") })
	})
	require.ErrorContains(t, err, "aborted")
	require.Contains(t, out, "These local platforms are not in use and will be deleted with all their data:")
	require.Contains(t, out, "  skali-dev                        legacy, skalid v0.1.0-rc.2     stopped  created before per-release platforms")
	require.Contains(t, out, "  skali-dev-v0-4-0                 skalid v0.4.0                  running")
	require.Contains(t, out, "  skali-dev-working-tree           working tree                   absent")
	require.NotContains(t, out, "skali-dev-v0-5-0")
	require.Contains(t, out, "Delete these local platforms and their data? [y/N]")
	require.Empty(t, *deleted)
	_, err = os.Stat(filepath.Join(stateHome, "skali", "localdev.json"))
	require.NoError(t, err)

	out, err = runCapturingStdout(t, func() error {
		return withStdin(t, "y\n", func() error { return execute(newRootCommand(), "dev", "prune") })
	})
	require.NoError(t, err)
	require.Contains(t, out, "Delete cluster skali-dev and volumes")
	require.Contains(t, out, "Delete cluster skali-dev-v0-4-0 and volumes")
	require.Contains(t, out, "Remove record skali-dev-working-tree")
	require.Equal(t, []string{"skali-dev", "skali-dev-v0-4-0"}, *deleted, "a record without a cluster is only a record")
	records, err := localdev.Records()
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "skali-dev-v0-5-0", records[0].Name)
	require.Nil(t, loadConfig(t).Remotes[localRemoteName], "the pruned platform's login is dropped")

	out, err = runCapturingStdout(t, func() error { return execute(newRootCommand(), "dev", "prune", "--yes") })
	require.NoError(t, err)
	require.Contains(t, out, "nothing to prune")
}

func TestDevRemoteLocalRefused(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	err := execute(newRootCommand(), "dev", "status", "--remote", "local")
	require.ErrorContains(t, err, "--remote local names the platform dev runs, not a release")
}
