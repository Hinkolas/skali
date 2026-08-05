package backup

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The tar/untar pair must roundtrip a volume exactly: contents, modes,
// nested directories, and symlinks, with lost+found left alone.
func TestTarUntarRoundtrip(t *testing.T) {
	source := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(source, "nested", "deep"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "top.txt"), []byte("top"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(source, "nested", "deep", "file.bin"),
		bytes.Repeat([]byte{0xAB}, 1<<16), 0o600))
	require.NoError(t, os.Symlink("top.txt", filepath.Join(source, "link")))
	require.NoError(t, os.MkdirAll(filepath.Join(source, "lost+found"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(source, "lost+found", "fsck0"), []byte("x"), 0o600))

	var archive bytes.Buffer
	require.NoError(t, writeTar(source, &archive))

	destination := t.TempDir()
	// Pre-existing content must be cleared, lost+found preserved.
	require.NoError(t, os.WriteFile(filepath.Join(destination, "stale.txt"), []byte("old"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(destination, "lost+found"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(destination, "lost+found", "fsck1"), []byte("y"), 0o600))
	require.NoError(t, clearDirectory(destination))
	require.NoError(t, extractTar(destination, bytes.NewReader(archive.Bytes())))

	require.NoFileExists(t, filepath.Join(destination, "stale.txt"))
	// lost+found is filesystem-owned: never archived from the source,
	// never cleared at the destination.
	require.DirExists(t, filepath.Join(destination, "lost+found"))
	require.FileExists(t, filepath.Join(destination, "lost+found", "fsck1"))
	require.NoFileExists(t, filepath.Join(destination, "lost+found", "fsck0"))

	top, err := os.ReadFile(filepath.Join(destination, "top.txt"))
	require.NoError(t, err)
	require.Equal(t, "top", string(top))

	deep, err := os.ReadFile(filepath.Join(destination, "nested", "deep", "file.bin"))
	require.NoError(t, err)
	require.Len(t, deep, 1<<16)
	info, err := os.Stat(filepath.Join(destination, "nested", "deep", "file.bin"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	link, err := os.Readlink(filepath.Join(destination, "link"))
	require.NoError(t, err)
	require.Equal(t, "top.txt", link)
}

func TestSecurePathRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"../escape", "a/../../escape", "/abs/../../escape"} {
		_, err := securePath(root, name)
		require.Error(t, err, name)
	}
	inside, err := securePath(root, "a/b/c.txt")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, "a", "b", "c.txt"), inside)
}

func TestFileSHA256(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	require.NoError(t, os.WriteFile(path, []byte("hello"), 0o644))
	digest, size, err := fileSHA256(path)
	require.NoError(t, err)
	require.EqualValues(t, 5, size)
	require.Equal(t, "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824", digest)
}
