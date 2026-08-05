package localdev

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeK3dScript is a stand-in binary that answers `k3d version` like the
// real one; installK3d smoke-probes what it lands, so the fake must run.
func fakeK3dScript(version string) string {
	return "#!/bin/sh\necho 'k3d version " + version + "'\necho 'k3s version v1.33.6-k3s1 (default)'\n"
}

func withHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	original := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { userHomeDir = original })
	return home
}

func withPathK3d(t *testing.T, path string, err error) {
	t.Helper()
	original := lookPath
	lookPath = func(string) (string, error) { return path, err }
	t.Cleanup(func() { lookPath = original })
}

func withK3dDownload(t *testing.T, body string) {
	t.Helper()
	originalGet := httpGet
	httpGet = func(context.Context, string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(body)), nil
	}
	sum := sha256.Sum256([]byte(body))
	platform := runtime.GOOS + "/" + runtime.GOARCH
	originalSHA := k3dSHA256[platform]
	k3dSHA256[platform] = hex.EncodeToString(sum[:])
	t.Cleanup(func() {
		httpGet = originalGet
		k3dSHA256[platform] = originalSHA
	})
}

// writeManagedK3d plants a runnable fake at the managed path, as a
// previous installK3d would have.
func writeManagedK3d(t *testing.T, version string) string {
	t.Helper()
	path, err := managedK3dPath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(fakeK3dScript(version)), 0o755))
	return path
}

func TestInstallK3dLandsManagedBinary(t *testing.T) {
	home := withHome(t)
	withPathK3d(t, "", errors.New("not found"))
	body := fakeK3dScript("v" + k3dPinnedVersion)
	withK3dDownload(t, body)

	path, err := installK3d(context.Background())
	require.NoError(t, err)
	require.Equal(t, filepath.Join(home, ".local", "share", "skali", "bin", "k3d"), path)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, body, string(data))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o755), info.Mode().Perm())

	require.Equal(t, path, k3dBinary(), "the managed binary must resolve for every lifecycle call")
	version, err := checkK3d(context.Background())
	require.NoError(t, err)
	require.Equal(t, "v"+k3dPinnedVersion, version)
}

func TestInstallK3dChecksumMismatch(t *testing.T) {
	withHome(t)
	withK3dDownload(t, fakeK3dScript("v"+k3dPinnedVersion))
	platform := runtime.GOOS + "/" + runtime.GOARCH
	k3dSHA256[platform] = strings.Repeat("00", 32)

	_, err := installK3d(context.Background())
	require.ErrorContains(t, err, "checksum mismatch")
	path, pathErr := managedK3dPath()
	require.NoError(t, pathErr)
	require.NoFileExists(t, path, "a failed verification must not land a binary")
	leftovers, globErr := filepath.Glob(filepath.Join(filepath.Dir(path), "k3d-download-*"))
	require.NoError(t, globErr)
	require.Empty(t, leftovers, "a failed download must not leave partial files")
}

func TestCheckK3dMissingEverywhere(t *testing.T) {
	withHome(t)
	withPathK3d(t, "", errors.New("not found"))
	_, err := checkK3d(context.Background())
	require.ErrorIs(t, err, errK3dMissing)
	require.Equal(t, "k3d", k3dBinary())
}

func TestCheckK3dManagedStalePinReinstalls(t *testing.T) {
	withHome(t)
	withPathK3d(t, "", errors.New("not found"))
	writeManagedK3d(t, "v5.8.3")
	_, err := checkK3d(context.Background())
	require.ErrorIs(t, err, errK3dMissing, "a managed binary off the pin is reinstalled, never trusted")
}

func TestCheckK3dPathWinsOverManaged(t *testing.T) {
	withHome(t)
	writeManagedK3d(t, "v"+k3dPinnedVersion)
	script := filepath.Join(t.TempDir(), "k3d")
	require.NoError(t, os.WriteFile(script, []byte(fakeK3dScript("v5.7.4")), 0o755))
	withPathK3d(t, script, nil)

	require.Equal(t, script, k3dBinary())
	version, err := checkK3d(context.Background())
	require.NoError(t, err)
	require.Equal(t, "v5.7.4", version, "a user's k3d on PATH always wins over the managed pin")
}

func TestCheckK3dPathTooOld(t *testing.T) {
	withHome(t)
	script := filepath.Join(t.TempDir(), "k3d")
	require.NoError(t, os.WriteFile(script, []byte(fakeK3dScript("v5.5.0")), 0o755))
	withPathK3d(t, script, nil)

	_, err := checkK3d(context.Background())
	require.ErrorContains(t, err, "too old")
	require.NotErrorIs(t, err, errK3dMissing, "an old user k3d is never shadowed by an install")
}

func TestVersionAtLeast(t *testing.T) {
	cases := []struct {
		version, minimum string
		want             bool
	}{
		{"v5.9.0", "5.6.0", true},
		{"v5.6.0", "5.6.0", true},
		{"v5.5.1", "5.6.0", false},
		{"v5.10.0", "5.6.0", true}, // numeric, not lexical
		{"v5.6", "5.6.0", true},
		{"v4.4.8", "5.6.0", false},
		{"v6.0.0-rc.1", "5.6.0", true},
		{"5.7.2", "5.6.0", true},
		{"weird", "5.6.0", true}, // unjudgeable versions are accepted
		{"", "5.6.0", true},
	}
	for _, c := range cases {
		require.Equal(t, c.want, versionAtLeast(c.version, c.minimum),
			"versionAtLeast(%q, %q)", c.version, c.minimum)
	}
}

func TestVersionOlder(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v0.1.0", "v0.2.0", true},
		{"v0.2.0", "v0.1.0", false},
		{"v0.2.0", "v0.2.0", false},
		{"v0.2.0", "v0.10.0", true}, // numeric, not lexical
		{"0.1.0", "v0.2.0", true},   // leading v optional
		{"v0.9.9", "v1.0.0", true},
		{"weird", "v0.2.0", false}, // unjudgeable versions never report drift
		{"v0.1.0", "weird", false},
		{"", "v0.2.0", false},
	}
	for _, c := range cases {
		require.Equal(t, c.want, VersionOlder(c.a, c.b),
			"VersionOlder(%q, %q)", c.a, c.b)
	}
}

func TestK3dVersionString(t *testing.T) {
	out := "k3d version v5.9.0\nk3s version v1.33.6-k3s1 (default)\n"
	require.Equal(t, "v5.9.0", k3dVersionString(out))
	require.Equal(t, "", k3dVersionString("something unexpected\n"))
}
