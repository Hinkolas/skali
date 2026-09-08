//go:build linux

package backup

import (
	"bytes"
	"github.com/stretchr/testify/require"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

// Run this as root in the disposable volume-test Job. The final command runs
// as the application's numeric identity, proving actual access after restore.
func TestLinuxNonRootVolumeRestore(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires Linux restore worker privileges")
	}
	base, err := os.MkdirTemp(os.Getenv("TEST_VOLUME_ROOT"), "skali-restore-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	require.NoError(t, os.Chmod(base, 0755))
	source, destination := filepath.Join(base, "source"), filepath.Join(base, "restored")
	require.NoError(t, os.Mkdir(source, 0700))
	require.NoError(t, os.Mkdir(destination, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(source, "private"), []byte("before"), 0600))
	require.NoError(t, os.Chown(source, 12345, 12345))
	require.NoError(t, os.Chown(filepath.Join(source, "private"), 12345, 12345))
	var data bytes.Buffer
	require.NoError(t, writeTar(source, &data))
	require.NoError(t, extractTar(destination, &data))
	cmd := exec.Command("sh", "-c", `test "$(cat "$1/private")" = before && printf after > "$1/private"`, "restore-check", destination)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 12345, Gid: 12345}}
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	result, err := os.ReadFile(filepath.Join(destination, "private"))
	require.NoError(t, err)
	require.Equal(t, "after", string(result))
}
