package mirror

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInstallDockerCerts(t *testing.T) {
	base := t.TempDir()
	ca, cert, key := []byte("ca-pem"), []byte("cert-pem"), []byte("key-pem")

	require.NoError(t, InstallDockerCerts(base, "10.0.0.1:5000", ca, cert, key))

	dir := filepath.Join(base, "10.0.0.1:5000")
	for _, tc := range []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{"ca.crt", ca, 0o644},
		{"client.cert", cert, 0o644},
		{"client.key", key, 0o600},
	} {
		got, err := os.ReadFile(filepath.Join(dir, tc.name))
		require.NoError(t, err, tc.name)
		require.Equal(t, tc.data, got, tc.name)
		info, err := os.Stat(filepath.Join(dir, tc.name))
		require.NoError(t, err)
		require.Equal(t, tc.mode, info.Mode().Perm(), tc.name)
	}

	// Reinstall (re-enroll) overwrites in place.
	require.NoError(t, InstallDockerCerts(base, "10.0.0.1:5000", []byte("ca2"), cert, key))
	got, err := os.ReadFile(filepath.Join(dir, "ca.crt"))
	require.NoError(t, err)
	require.Equal(t, []byte("ca2"), got)
}
