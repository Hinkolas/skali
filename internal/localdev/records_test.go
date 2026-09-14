package localdev

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/version"
)

func withVersion(t *testing.T, v string) {
	t.Helper()
	previous := version.Version
	version.Version = v
	t.Cleanup(func() { version.Version = previous })
}

func TestClusterNameDoesNotChangeWithRelease(t *testing.T) {
	withVersion(t, "v0.1.0-rc.3")
	require.Equal(t, "skali-dev", ClusterName())
	withVersion(t, "v0.2.0")
	require.Equal(t, "skali-dev", ClusterName())
	withVersion(t, "v0.0.0-dev")
	require.Equal(t, "skali-dev", ClusterName())
	t.Setenv("SKALI_DEV_CLUSTER", "skali-dev-e2e")
	require.Equal(t, "skali-dev-e2e", ClusterName())
}

func TestStatePathsPerCluster(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", home)
	withVersion(t, "v0.1.0-rc.3")
	path, err := statePath()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(home, "skali", "dev", "skali-dev", "state.json"), path)
	kubeconfig, err := KubeconfigPath()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(home, "skali", "dev", "skali-dev", "kubeconfig"), kubeconfig)

	_, err = LoadState()
	require.ErrorIs(t, err, ErrNotInstalled)
	state, err := NewState("ghcr.io/hinkolas/skalid:v0.1.0-rc.3")
	require.NoError(t, err)
	require.Equal(t, "v0.1.0-rc.3", state.Version)
	require.Equal(t, "skali-dev", state.Cluster)
	require.NoError(t, SaveState(state))
	loaded, err := LoadState()
	require.NoError(t, err)
	require.Equal(t, state.AuthSecret, loaded.AuthSecret)
	require.NoError(t, RemoveState())
	_, err = os.Stat(filepath.Dir(path))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func writeRecord(t *testing.T, path string, state State) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	raw, err := json.Marshal(state)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, raw, 0o600))
}

func TestRecords(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", home)
	root := filepath.Join(home, "skali")
	created := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)

	records, err := Records()
	require.NoError(t, err)
	require.Empty(t, records)

	writeRecord(t, filepath.Join(root, "dev", "skali-dev-v0-1-0-rc-3", "state.json"), State{
		Cluster: "skali-dev-v0-1-0-rc-3", Version: "v0.1.0-rc.3",
		SkalidImage: "ghcr.io/hinkolas/skalid:v0.1.0-rc.3", K3sImage: K3sImage, CreatedAt: created,
	})
	writeRecord(t, filepath.Join(root, "dev", "skali-dev-working-tree", "state.json"), State{
		Cluster: "skali-dev-working-tree", SkalidImage: "skalid:dev", K3sImage: K3sImage, CreatedAt: created,
	})
	// A directory without a record is not a platform.
	require.NoError(t, os.MkdirAll(filepath.Join(root, "dev", "stray"), 0o700))
	// The legacy record implies its release through the image.
	writeRecord(t, filepath.Join(root, "localdev.json"), State{
		Cluster: "skali-dev", SkalidImage: "ghcr.io/hinkolas/skalid:v0.1.0-rc.2", K3sImage: "rancher/k3s:v1.35.0-k3s1", CreatedAt: created,
	})
	require.NoError(t, os.WriteFile(filepath.Join(root, "kubeconfig"), []byte("k"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "registries.yaml"), []byte("r"), 0o600))

	records, err = Records()
	require.NoError(t, err)
	require.Equal(t, []Record{
		{Name: "skali-dev", Version: "v0.1.0-rc.2", SkalidImage: "ghcr.io/hinkolas/skalid:v0.1.0-rc.2",
			K3sImage: "rancher/k3s:v1.35.0-k3s1", CreatedAt: created, Legacy: true},
		{Name: "skali-dev-v0-1-0-rc-3", Version: "v0.1.0-rc.3", SkalidImage: "ghcr.io/hinkolas/skalid:v0.1.0-rc.3",
			K3sImage: K3sImage, CreatedAt: created},
		{Name: "skali-dev-working-tree", SkalidImage: "skalid:dev", K3sImage: K3sImage, CreatedAt: created},
	}, records)

	require.ErrorContains(t, ObsoletePlatforms(), "k3d cluster delete skali-dev-v0-1-0-rc-3")
	require.FileExists(t, filepath.Join(root, "localdev.json"))
	require.FileExists(t, filepath.Join(root, "dev", "skali-dev-v0-1-0-rc-3", "state.json"))
}

func TestParseClusterListAndClustersToStop(t *testing.T) {
	t.Parallel()
	statuses, err := parseClusterList([]byte(`[
		{"name":"skali-dev","nodes":[{"State":{"Running":true}}]},
		{"name":"skali-dev-v0-1-0-rc-3","nodes":[{"State":{"Running":false}}]},
		{"name":"skali-test","nodes":[{"State":{"Running":true}}]}]`))
	require.NoError(t, err)
	require.Equal(t, ClusterRunning, StatusOf(statuses, "skali-dev"))
	require.Equal(t, ClusterStopped, StatusOf(statuses, "skali-dev-v0-1-0-rc-3"))
	require.Equal(t, ClusterAbsent, StatusOf(statuses, "skali-dev-working-tree"))

	_, err = parseClusterList([]byte("nonsense"))
	require.Error(t, err)
}

func TestVersionMismatchLeavesRecordUnchanged(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	withVersion(t, "v0.4.0")
	state, err := NewState(version.PublishedSkalidImage("v0.4.0"))
	require.NoError(t, err)
	require.NoError(t, SaveState(state))
	withVersion(t, "v0.5.0-rc.1")
	require.ErrorContains(t, CheckVersion(state), "skali dev reset")
	actual, err := LoadState()
	require.NoError(t, err)
	require.Equal(t, state.Version, actual.Version)
	require.True(t, state.CreatedAt.Equal(actual.CreatedAt))
	withVersion(t, "v0.4.0")
	require.NoError(t, CheckVersion(state))
	state.K3sImage = "old"
	require.ErrorContains(t, CheckVersion(state), "skali dev reset")
	withVersion(t, "v0.0.0-dev")
	require.ErrorContains(t, CheckVersion(state), "working tree")
}
