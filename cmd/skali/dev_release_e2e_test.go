package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Real stamped CLI/daemon pairs exercise the same fixed cluster, ports and
// installation record. Nothing uses the developer's own dev cluster.
func TestDevSingleReleaseEndToEnd(t *testing.T) {
	h := newE2EHarness(t)
	repo, err := filepath.Abs("../..")
	require.NoError(t, err)
	versions := []string{"v0.1.0-rc.3", "v0.1.0-rc.4"}
	binaries := make([]string, len(versions))
	for i, release := range versions {
		binaries[i] = filepath.Join(t.TempDir(), "skali")
		build := exec.Command("go", "build", "-ldflags", "-X github.com/Hinkolas/skali/internal/version.Version="+release, "-o", binaries[i], "./cmd/skali")
		build.Dir = repo
		out, err := build.CombinedOutput()
		require.NoError(t, err, "%s", out)
		image := exec.Command("docker", "build", "--build-arg", "VERSION="+release, "-t", "ghcr.io/hinkolas/skalid:"+release, "-f", filepath.Join(repo, "build/skalid.Dockerfile"), repo)
		out, err = image.CombinedOutput()
		require.NoError(t, err, "%s", out)
	}
	h.binary = binaries[0]
	out := h.run(false, "", "dev", "start", "--offline")
	require.Contains(t, out, "Create k3d cluster "+e2eCluster)
	record := filepath.Join(h.stateDir(), "skali", "dev", e2eCluster, "state.json")
	before, err := os.ReadFile(record)
	require.NoError(t, err)
	require.Contains(t, string(before), versions[0])
	h.binary = binaries[1]
	out = h.run(true, "", "dev", "start", "--offline")
	require.Contains(t, out, "skali dev reset")
	after, err := os.ReadFile(record)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Contains(t, h.run(false, "", "dev", "status"), versions[0])
	h.binary = binaries[0]
	require.Contains(t, h.run(false, "", "dev", "stop"), "state is retained")
	require.Contains(t, h.run(false, "", "dev", "stop"), "already stopped")
	require.Contains(t, h.run(false, "", "dev", "start", "--offline"), "state retained")
	h.binary = binaries[1]
	h.run(true, "\n", "dev", "reset")
	require.FileExists(t, record)
	h.run(false, "", "dev", "reset", "--yes")
	require.NoFileExists(t, record)
	h.run(false, "", "dev", "start", "--offline")
	after, err = os.ReadFile(record)
	require.NoError(t, err)
	require.Contains(t, string(after), versions[1])
	// Explicit reset remains an escape hatch if the installation record is lost.
	require.NoError(t, os.Remove(record))
	h.run(true, "\n", "dev", "reset")
	require.Contains(t, h.run(false, "", "dev", "status"), "no installation record")
	h.run(false, "", "dev", "reset", "--yes")
	require.NoFileExists(t, record)
}
