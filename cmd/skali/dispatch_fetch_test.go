package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/installer"
)

func assertCached(t *testing.T, cacheDir, release string, body []byte) {
	t.Helper()
	data, ok := installer.CachedBinary(installer.CLICachePath(cacheDir, release))
	require.True(t, ok, "release %s cached", release)
	require.Equal(t, body, data)
}

func TestEnsureCLIFromFeed(t *testing.T) {
	binary := fakeCLI("v0.4.0")
	f := newDispatchFixture(t, "v0.5.0")
	feed := f.serveFeed(t, "v0.4.0")

	fetch, err := f.d.ensureCLI(context.Background(), "khz", "v0.4.0")
	require.NoError(t, err)
	require.True(t, fetch.fetched)
	require.Equal(t, "v0.4.0", fetch.version)
	require.Equal(t, binary, fetch.binary)
	require.Equal(t, installer.CLICachePath(f.cache, "v0.4.0"), fetch.path)
	assertCached(t, f.cache, "v0.4.0", binary)
	require.Contains(t, feed.requested(), "/Hinkolas/skali/releases/download/v0.4.0/checksums.txt")
	require.Equal(t, "fetching skali v0.4.0 for remote khz\n", f.stderr.String())

	// The second call is a cache hit and never touches the feed.
	before := len(feed.requested())
	fetch, err = f.d.ensureCLI(context.Background(), "khz", "v0.4.0")
	require.NoError(t, err)
	require.False(t, fetch.fetched)
	require.Len(t, feed.requested(), before)
}

func TestEnsureCLIFeedChecksumMismatch(t *testing.T) {
	assets := releaseAssets(fakeCLI("v0.4.0"))
	assets["checksums.txt"] = []byte(checksumEntry(installer.CLIAsset(runtime.GOOS, runtime.GOARCH), []byte("tampered")))
	feed := newFakeUpgradeServer(t, "v0.4.0", assets)
	f := newDispatchFixture(t, "v0.5.0")
	f.d.releaseBase, f.d.feedClient = feed.URL, feed.Client()

	_, err := f.d.ensureCLI(context.Background(), "khz", "v0.4.0")
	require.Error(t, err)
	f.d.warn(err)
	require.Contains(t, f.stderr.String(), "warning: skali v0.4.0 from the release feed did not match its published checksum; running skali v0.5.0")
	_, ok := installer.CachedBinary(installer.CLICachePath(f.cache, "v0.4.0"))
	require.False(t, ok, "nothing is cached from a bad download")
}

func TestEnsureCLIReleaseMissing(t *testing.T) {
	f := newDispatchFixture(t, "v0.5.0")
	f.serveFeed(t, "v0.9.0") // v0.4.0 is not on the feed

	_, err := f.d.ensureCLI(context.Background(), "khz", "v0.4.0")
	f.d.warn(err)
	require.Contains(t, f.stderr.String(), "warning: the release feed has no skali v0.4.0 (release v0.4.0 was not found); "+
		"install it from your own distribution; running skali v0.5.0")
}

func TestEnsureCLIFeedUnreachable(t *testing.T) {
	f := newDispatchFixture(t, "v0.5.0") // the fixture's feed is a dead URL

	_, err := f.d.ensureCLI(context.Background(), "khz", "v0.4.0")
	require.Error(t, err)
	f.d.warn(err)
	require.Contains(t, f.stderr.String(), "warning: could not fetch skali v0.4.0 from the release feed: ")
	require.Contains(t, f.stderr.String(), "; running skali v0.5.0")
}

func TestPromoteHomeReplacesWritableExecutable(t *testing.T) {
	f := newDispatchFixture(t, "v0.3.2")
	dir := t.TempDir()
	f.d.executable = writeExecutable(t, dir, "skali", fakeCLI("v0.3.2"))

	require.True(t, f.d.promoteHome(context.Background(), fakeCLI("v0.4.0"), "v0.4.0", "khz"))
	installed, err := os.ReadFile(f.d.executable)
	require.NoError(t, err)
	require.Equal(t, fakeCLI("v0.4.0"), installed)
	require.Equal(t, "upgraded skali v0.3.2 -> v0.4.0 (remote khz runs skalid v0.4.0)\n", f.stderr.String())
}

func TestPromoteHomeUnwritableDirPrintsHint(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	f := newDispatchFixture(t, "v0.3.2")
	dir := t.TempDir()
	f.d.executable = writeExecutable(t, dir, "skali", fakeCLI("v0.3.2"))
	require.NoError(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	require.False(t, f.d.promoteHome(context.Background(), fakeCLI("v0.4.0"), "v0.4.0", "khz"))
	require.Contains(t, f.stderr.String(), "hint: skali v0.4.0 is cached but "+dir+" is not writable; run sudo skali upgrade --version v0.4.0 once to make it the default")
	unchanged, err := os.ReadFile(f.d.executable)
	require.NoError(t, err)
	require.Equal(t, fakeCLI("v0.3.2"), unchanged)
}

func TestPromoteHomeVerifyFailureRestores(t *testing.T) {
	f := newDispatchFixture(t, "v0.3.2")
	dir := t.TempDir()
	f.d.executable = writeExecutable(t, dir, "skali", fakeCLI("v0.3.2"))

	require.False(t, f.d.promoteHome(context.Background(), fakeCLI("v0.3.9"), "v0.4.0", "khz"))
	require.Contains(t, f.stderr.String(), "warning: could not replace "+f.d.executable+" with skali v0.4.0:")
	require.Contains(t, f.stderr.String(), "the previous binary was restored; running the cached copy")
	restored, err := os.ReadFile(f.d.executable)
	require.NoError(t, err)
	require.Equal(t, fakeCLI("v0.3.2"), restored)
	entries, err := os.ReadDir(filepath.Dir(f.d.executable))
	require.NoError(t, err)
	require.Len(t, entries, 1, "no temp files left behind")
}
