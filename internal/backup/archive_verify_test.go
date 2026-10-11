package backup

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// volumeArchive archives a small tree the way a volume backup does and
// returns it with its sha256.
func volumeArchive(t *testing.T) ([]byte, string) {
	t.Helper()
	source := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(source, "nested"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "nested", "file"), bytes.Repeat([]byte("x"), 4096), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(source, "top"), []byte("restored"), 0o644))
	var archive bytes.Buffer
	require.NoError(t, writeTar(source, &archive))
	sum := sha256.Sum256(archive.Bytes())
	return archive.Bytes(), hex.EncodeToString(sum[:])
}

// Verifying an archive reads all of it: the stored bytes must be the
// size and checksum the snapshot records, the compression frame's own
// checksum must hold, and every entry must be one extraction accepts.
func TestVerifyArchive(t *testing.T) {
	archive, digest := volumeArchive(t)
	size := int64(len(archive))
	summary, err := verifyArchive(bytes.NewReader(archive), objectCheck{Bytes: size, SHA256: digest})
	require.NoError(t, err)
	require.EqualValues(t, 4, summary.Entries, "root, nested, nested/file, top")
	require.EqualValues(t, 4096+8, summary.Bytes)
	_, err = verifyArchive(bytes.NewReader(archive), objectCheck{Bytes: -1})
	require.NoError(t, err, "an unknown size and no checksum still verify the archive itself")

	flipped := bytes.Clone(archive)
	flipped[len(flipped)/2] ^= 0xff
	for name, tc := range map[string]struct {
		data   []byte
		expect objectCheck
	}{
		"truncated":        {archive[:len(archive)-16], objectCheck{Bytes: -1}},
		"corrupted":        {flipped, objectCheck{Bytes: -1}},
		"size differs":     {archive, objectCheck{Bytes: size + 1}},
		"checksum differs": {archive, objectCheck{Bytes: size, SHA256: strings.Repeat("0", 64)}},
		"not an archive":   {[]byte("plain text, not zstd"), objectCheck{Bytes: -1}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := verifyArchive(bytes.NewReader(tc.data), tc.expect)
			require.Error(t, err)
		})
	}
}

// The shapes extraction refuses are refused by verification already, so a
// hostile archive fails before the volume is cleared.
func TestVerifyArchiveRejectsUnsafeShapes(t *testing.T) {
	for name, headers := range map[string][]*tar.Header{
		"parent":    {{Name: "../escaped", Typeflag: tar.TypeReg, Size: 1}},
		"protected": {{Name: "lost+found/escaped", Typeflag: tar.TypeReg, Size: 1}},
		"duplicate": {{Name: "a", Typeflag: tar.TypeReg}, {Name: "a", Typeflag: tar.TypeReg}},
		"device":    {{Name: "device", Typeflag: tar.TypeChar}},
		"pivot": {{Name: "pivot", Typeflag: tar.TypeSymlink, Linkname: "/"},
			{Name: "pivot/escaped", Typeflag: tar.TypeReg, Size: 1}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := verifyArchive(bytes.NewReader(archiveFixture(t, headers...)), objectCheck{Bytes: -1})
			require.Error(t, err)
		})
	}
}

// A restore whose archive fails verification leaves the volume exactly as
// it was; one that verifies replaces the contents. Each pass opens the
// archive anew.
func TestRestoreArchiveVerifiesBeforeClearing(t *testing.T) {
	archive, digest := volumeArchive(t)
	opener := func(data []byte, opened *int) func() (io.ReadCloser, error) {
		return func() (io.ReadCloser, error) {
			*opened++
			return io.NopCloser(bytes.NewReader(data)), nil
		}
	}

	volume := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(volume, "current"), []byte("live data"), 0o644))
	var opened int
	err := restoreArchive(volume, opener(archive[:len(archive)-16], &opened),
		objectCheck{Bytes: int64(len(archive)), SHA256: digest})
	require.ErrorContains(t, err, "the volume was not touched")
	require.Equal(t, 1, opened, "a failed verification never opens the archive for extraction")
	current, err := os.ReadFile(filepath.Join(volume, "current"))
	require.NoError(t, err)
	require.Equal(t, "live data", string(current))

	opened = 0
	require.NoError(t, restoreArchive(volume, opener(archive, &opened),
		objectCheck{Bytes: int64(len(archive)), SHA256: digest}))
	require.Equal(t, 2, opened)
	require.NoFileExists(t, filepath.Join(volume, "current"))
	top, err := os.ReadFile(filepath.Join(volume, "top"))
	require.NoError(t, err)
	require.Equal(t, "restored", string(top))

	unreachable := errors.New("target unreachable")
	err = restoreArchive(volume, func() (io.ReadCloser, error) { return nil, unreachable }, objectCheck{Bytes: -1})
	require.ErrorIs(t, err, unreachable)
	require.FileExists(t, filepath.Join(volume, "top"))
}
