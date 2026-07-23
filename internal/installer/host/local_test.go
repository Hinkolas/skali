package host

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLocalReplaceFileKeepsLastContentsAndMode(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "installation.yaml")
	backup := filepath.Join(dir, "installation.yaml.prev")
	require.NoError(t, os.WriteFile(path, []byte("previous"), 0o644))

	require.NoError(t, (Local{}).ReplaceFile(
		context.Background(), path, backup, []byte("current"), 0o600))

	current, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "current", string(current))
	previous, err := os.ReadFile(backup)
	require.NoError(t, err)
	require.Equal(t, "previous", string(previous))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}
