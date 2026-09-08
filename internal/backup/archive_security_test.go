package backup

import (
	"archive/tar"
	"bytes"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func archiveFixture(t *testing.T, headers ...*tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	require.NoError(t, err)
	tw := tar.NewWriter(zw)
	for _, h := range headers {
		h.Uid = os.Getuid()
		h.Gid = os.Getgid()
		h.ModTime = time.Unix(1700000000, 0)
		require.NoError(t, tw.WriteHeader(h))
		if h.Size > 0 {
			_, err = tw.Write(bytes.Repeat([]byte("x"), int(h.Size)))
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func TestArchiveRejectsUnsafeShapes(t *testing.T) {
	outside := t.TempDir()
	cases := map[string][]*tar.Header{
		"pivot":      {{Name: "pivot", Typeflag: tar.TypeSymlink, Linkname: outside}, {Name: "pivot/escaped", Typeflag: tar.TypeReg, Size: 1}},
		"late-pivot": {{Name: "pivot/escaped", Typeflag: tar.TypeReg, Size: 1}, {Name: "pivot", Typeflag: tar.TypeSymlink, Linkname: outside}},
		"absolute":   {{Name: outside + "/escaped", Typeflag: tar.TypeReg, Size: 1}},
		"parent":     {{Name: "../escaped", Typeflag: tar.TypeReg, Size: 1}},
		"protected":  {{Name: "lost+found/escaped", Typeflag: tar.TypeReg, Size: 1}},
		"duplicate":  {{Name: "a", Typeflag: tar.TypeReg}, {Name: "a", Typeflag: tar.TypeReg}},
		"device":     {{Name: "device", Typeflag: tar.TypeChar}},
		"hardlink":   {{Name: "link", Typeflag: tar.TypeLink, Linkname: "other"}},
	}
	for name, headers := range cases {
		t.Run(name, func(t *testing.T) {
			require.Error(t, extractTar(t.TempDir(), bytes.NewReader(archiveFixture(t, headers...))))
			require.NoFileExists(t, filepath.Join(outside, "escaped"))
		})
	}
}

func TestArchiveRestoresRootAndPrivateMetadata(t *testing.T) {
	source, destination := t.TempDir(), t.TempDir()
	require.NoError(t, os.Chmod(source, 0750))
	require.NoError(t, os.Mkdir(filepath.Join(source, "private"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(source, "private", "data"), []byte("private"), 0600))
	require.NoError(t, os.Symlink("private/data", filepath.Join(source, "link")))
	if os.Geteuid() == 0 {
		require.NoError(t, os.Chown(source, 12345, 12345))
		require.NoError(t, os.Chown(filepath.Join(source, "private"), 12345, 12345))
		require.NoError(t, os.Chown(filepath.Join(source, "private", "data"), 12345, 12345))
		require.NoError(t, os.Lchown(filepath.Join(source, "link"), 12345, 12345))
	}
	var buf bytes.Buffer
	require.NoError(t, writeTar(source, &buf))
	require.NoError(t, extractTar(destination, &buf))
	for _, name := range []string{".", "private", "private/data", "link"} {
		a, err := os.Lstat(filepath.Join(source, name))
		require.NoError(t, err)
		b, err := os.Lstat(filepath.Join(destination, name))
		require.NoError(t, err)
		require.Equal(t, a.Mode(), b.Mode())
		require.Equal(t, a.Sys().(*syscall.Stat_t).Uid, b.Sys().(*syscall.Stat_t).Uid)
		require.Equal(t, a.Sys().(*syscall.Stat_t).Gid, b.Sys().(*syscall.Stat_t).Gid)
	}
}
