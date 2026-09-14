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

func TestClusterNameFor(t *testing.T) {
	require.Equal(t, "skali-dev-v0-1-0-rc-3", ClusterNameFor("v0.1.0-rc.3"))
	require.Equal(t, "skali-dev-v1-2-3", ClusterNameFor("v1.2.3"))
	require.Equal(t, "skali-dev-working-tree", ClusterNameFor(""))

	// k3d caps cluster names at 32 characters (RFC 1123 hostname); the
	// widest tag the release shape allows in practice stays inside it.
	require.LessOrEqual(t, len(ClusterNameFor("v100.100.100-alpha.100")), 32)

	withVersion(t, "v0.1.0-rc.3")
	require.Equal(t, "v0.1.0-rc.3", PlatformVersion())
	require.Equal(t, "skali-dev-v0-1-0-rc-3", ClusterName())
	withVersion(t, "v0.1.0-rc.2-5-gabc1234")
	require.Equal(t, "", PlatformVersion())
	require.Equal(t, "skali-dev-working-tree", ClusterName())

	t.Setenv("SKALI_DEV_CLUSTER", "skali-dev-e2e")
	require.Equal(t, "skali-dev-e2e", ClusterName())
}

func TestStatePathsPerCluster(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", home)
	withVersion(t, "v0.1.0-rc.3")
	path, err := statePath()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(home, "skali", "dev", "skali-dev-v0-1-0-rc-3", "state.json"), path)
	kubeconfig, err := KubeconfigPath()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(home, "skali", "dev", "skali-dev-v0-1-0-rc-3", "kubeconfig"), kubeconfig)

	_, err = LoadState()
	require.ErrorIs(t, err, ErrNotInstalled)
	state, err := NewState("ghcr.io/hinkolas/skalid:v0.1.0-rc.3")
	require.NoError(t, err)
	require.Equal(t, "v0.1.0-rc.3", state.Version)
	require.Equal(t, "skali-dev-v0-1-0-rc-3", state.Cluster)
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

	require.NoError(t, RemoveLegacyRecord())
	for _, file := range []string{"localdev.json", "kubeconfig", "registries.yaml"} {
		_, err := os.Stat(filepath.Join(root, file))
		require.ErrorIs(t, err, os.ErrNotExist, file)
	}
	_, err = os.Stat(filepath.Join(root, "dev", "skali-dev-working-tree", "state.json"))
	require.NoError(t, err, "the per-cluster records survive a legacy removal")
	require.NoError(t, RemoveRecord("skali-dev-v0-1-0-rc-3"))
	records, err = Records()
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "skali-dev-working-tree", records[0].Name)
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

	records := []Record{
		{Name: "skali-dev", Legacy: true},
		{Name: "skali-dev-v0-1-0-rc-3", Version: "v0.1.0-rc.3"},
		{Name: "skali-dev-working-tree"},
	}
	// Only recorded platforms are stopped: skali-test runs but is foreign.
	require.Equal(t, []string{"skali-dev"}, clustersToStop(records, statuses, "skali-dev-working-tree"))
	require.Empty(t, clustersToStop(records, statuses, "skali-dev"))

	_, err = parseClusterList([]byte("nonsense"))
	require.Error(t, err)
}
