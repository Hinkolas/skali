package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/installer"
)

// fakeCluster plays a skalid serving its CLI: the health probe, the
// listing, and the download, all stamped with its version and identity.
// Knobs make it refuse in each way the dispatcher must survive.
type fakeCluster struct {
	*httptest.Server
	version string
	binary  []byte

	mu           sync.Mutex
	downloads    int
	disabled     bool
	unauthorized bool
	badSum       bool
}

func newFakeCluster(t *testing.T, version string, binary []byte) *fakeCluster {
	t.Helper()
	f := &fakeCluster{version: version, binary: binary}
	platform := client.CLIPlatform(runtime.GOOS, runtime.GOARCH)
	mux := http.NewServeMux()
	stamp := func(w http.ResponseWriter) {
		w.Header().Set(client.VersionHeader, f.version)
		w.Header().Set(client.InstanceHeader, "inst-1")
	}
	refuse := func(w http.ResponseWriter, status int, code, message string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": message}})
	}
	authorized := func(w http.ResponseWriter, r *http.Request) bool {
		f.mu.Lock()
		unauthorized := f.unauthorized
		f.mu.Unlock()
		if unauthorized || r.Header.Get("Authorization") != "Bearer tok" {
			refuse(w, http.StatusUnauthorized, "invalid_token", "session expired")
			return false
		}
		return true
	}
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		stamp(w)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/v1/system/cli", func(w http.ResponseWriter, r *http.Request) {
		stamp(w)
		if !authorized(w, r) {
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.disabled {
			_ = json.NewEncoder(w).Encode(map[string]any{"version": f.version, "enabled": false, "reason": "disabled", "platforms": []any{}})
			return
		}
		sum := sha256.Sum256(f.binary)
		digest := hex.EncodeToString(sum[:])
		if f.badSum {
			digest = "deadbeef"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"version": f.version, "enabled": true, "platforms": []map[string]any{
			{"platform": platform, "sha256": digest, "size": len(f.binary)},
		}})
	})
	mux.HandleFunc("/v1/system/cli/"+platform, func(w http.ResponseWriter, r *http.Request) {
		stamp(w)
		if !authorized(w, r) {
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.disabled {
			refuse(w, http.StatusNotFound, client.CodeCLINotServed, "this cluster does not serve the skali CLI; install skali "+f.version)
			return
		}
		f.downloads++
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(f.binary)))
		_, _ = w.Write(f.binary)
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeCluster) set(mutate func(*fakeCluster)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	mutate(f)
}

func (f *fakeCluster) downloadCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.downloads
}

// fetchFixture prepares a dispatcher and a target remote for ensureCLI.
func fetchFixture(t *testing.T, master string) (*dispatchFixture, *remoteTarget) {
	t.Helper()
	f := newDispatchFixture(t, "v0.5.0")
	f.seedRemote(t, "khz", master, "v0.4.0")
	cfg := loadConfig(t)
	return f, &remoteTarget{Name: "khz", Remote: cfg.Remotes["khz"]}
}

func assertCached(t *testing.T, cacheDir, release string, body []byte) {
	t.Helper()
	data, ok := installer.CachedBinary(installer.CLICachePath(cacheDir, release))
	require.True(t, ok, "release %s cached", release)
	require.Equal(t, body, data)
}

func TestEnsureCLIFromCluster(t *testing.T) {
	binary := fakeCLI("v0.4.0")
	cluster := newFakeCluster(t, "v0.4.0", binary)
	feed := newFakeUpgradeServer(t, "v0.4.0", releaseAssets(binary))
	f, target := fetchFixture(t, cluster.URL)
	f.d.releaseBase, f.d.feedClient = feed.URL, feed.Client()

	fetch, err := f.d.ensureCLI(context.Background(), loadConfig(t), target, "v0.4.0")
	require.NoError(t, err)
	require.True(t, fetch.fetched)
	require.Equal(t, "v0.4.0", fetch.version)
	require.Equal(t, binary, fetch.binary)
	assertCached(t, f.cache, "v0.4.0", binary)
	require.Empty(t, feed.requested(), "the cluster is the primary source")
	require.Equal(t, 1, cluster.downloadCount())
	require.Equal(t, "fetching skali v0.4.0 for remote khz\n", f.stderr.String())

	// The second call is a cache hit.
	fetch, err = f.d.ensureCLI(context.Background(), loadConfig(t), target, "v0.4.0")
	require.NoError(t, err)
	require.False(t, fetch.fetched)
	require.Equal(t, 1, cluster.downloadCount())
}

func TestEnsureCLIFallsBackToFeed(t *testing.T) {
	binary := fakeCLI("v0.4.0")
	cases := []struct {
		name   string
		mutate func(*fakeCluster)
		token  string
	}{
		{"serving disabled", func(c *fakeCluster) { c.disabled = true }, "tok"},
		{"session invalid", func(c *fakeCluster) { c.unauthorized = true }, "tok"},
		{"no session", func(*fakeCluster) {}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cluster := newFakeCluster(t, "v0.4.0", binary)
			cluster.set(tc.mutate)
			feed := newFakeUpgradeServer(t, "v0.4.0", releaseAssets(binary))
			f, target := fetchFixture(t, cluster.URL)
			target.Remote.Token = tc.token
			f.d.releaseBase, f.d.feedClient = feed.URL, feed.Client()

			fetch, err := f.d.ensureCLI(context.Background(), loadConfig(t), target, "v0.4.0")
			require.NoError(t, err)
			require.True(t, fetch.fetched)
			assertCached(t, f.cache, "v0.4.0", binary)
			require.Contains(t, feed.requested(), "/Hinkolas/skali/releases/download/v0.4.0/checksums.txt")
			require.Equal(t, 0, cluster.downloadCount())
			require.NotContains(t, f.stderr.String(), "warning:")
		})
	}
}

func TestEnsureCLIUnreachableIsSilent(t *testing.T) {
	f, target := fetchFixture(t, deadURL(t))
	_, err := f.d.ensureCLI(context.Background(), loadConfig(t), target, "v0.4.0")
	require.ErrorIs(t, err, errSilentFetch)
	f.d.warn(err)
	require.Equal(t, "fetching skali v0.4.0 for remote khz\n", f.stderr.String(), "no warning of its own")
}

func TestEnsureCLIInstanceMismatchIsSilent(t *testing.T) {
	cluster := newFakeCluster(t, "v0.4.0", fakeCLI("v0.4.0"))
	f, target := fetchFixture(t, cluster.URL)
	target.Remote.Instance = "inst-0"
	_, err := f.d.ensureCLI(context.Background(), loadConfig(t), target, "v0.4.0")
	require.ErrorIs(t, err, errSilentFetch)
}

func TestEnsureCLIClusterChecksumMismatchUsesFeed(t *testing.T) {
	binary := fakeCLI("v0.4.0")
	cluster := newFakeCluster(t, "v0.4.0", binary)
	cluster.set(func(c *fakeCluster) { c.badSum = true })
	feed := newFakeUpgradeServer(t, "v0.4.0", releaseAssets(binary))
	f, target := fetchFixture(t, cluster.URL)
	f.d.releaseBase, f.d.feedClient = feed.URL, feed.Client()

	fetch, err := f.d.ensureCLI(context.Background(), loadConfig(t), target, "v0.4.0")
	require.NoError(t, err)
	require.True(t, fetch.fetched)
	assertCached(t, f.cache, "v0.4.0", binary)
	require.Contains(t, f.stderr.String(), "warning: skali v0.4.0 from remote khz did not match its checksum; fetched it from the release feed")
}

func TestEnsureCLIFeedChecksumMismatch(t *testing.T) {
	assets := releaseAssets(fakeCLI("v0.4.0"))
	assets["checksums.txt"] = []byte(checksumEntry(installer.CLIAsset(runtime.GOOS, runtime.GOARCH), []byte("tampered")))
	feed := newFakeUpgradeServer(t, "v0.4.0", assets)
	cluster := newFakeCluster(t, "v0.4.0", fakeCLI("v0.4.0"))
	cluster.set(func(c *fakeCluster) { c.disabled = true })
	f, target := fetchFixture(t, cluster.URL)
	f.d.releaseBase, f.d.feedClient = feed.URL, feed.Client()

	_, err := f.d.ensureCLI(context.Background(), loadConfig(t), target, "v0.4.0")
	require.Error(t, err)
	f.d.warn(err)
	require.Contains(t, f.stderr.String(), "warning: skali v0.4.0 from the release feed did not match its published checksum; running skali v0.5.0")
	_, ok := installer.CachedBinary(installer.CLICachePath(f.cache, "v0.4.0"))
	require.False(t, ok, "nothing is cached from a bad download")
}

func TestEnsureCLIDoubleFailureMessages(t *testing.T) {
	feed := newFakeUpgradeServer(t, "v0.9.0", nil) // v0.4.0 is not on the feed
	t.Run("not served", func(t *testing.T) {
		cluster := newFakeCluster(t, "v0.4.0", fakeCLI("v0.4.0"))
		cluster.set(func(c *fakeCluster) { c.disabled = true })
		f, target := fetchFixture(t, cluster.URL)
		f.d.releaseBase, f.d.feedClient = feed.URL, feed.Client()
		_, err := f.d.ensureCLI(context.Background(), loadConfig(t), target, "v0.4.0")
		f.d.warn(err)
		require.Contains(t, f.stderr.String(), "warning: remote khz does not serve its CLI and the release feed has no skali v0.4.0; "+
			"run skali upgrade --version v0.4.0 or install it from your own distribution; running skali v0.5.0")
	})
	t.Run("session invalid", func(t *testing.T) {
		cluster := newFakeCluster(t, "v0.4.0", fakeCLI("v0.4.0"))
		cluster.set(func(c *fakeCluster) { c.unauthorized = true })
		f, target := fetchFixture(t, cluster.URL)
		f.d.releaseBase, f.d.feedClient = feed.URL, feed.Client()
		_, err := f.d.ensureCLI(context.Background(), loadConfig(t), target, "v0.4.0")
		f.d.warn(err)
		require.Contains(t, f.stderr.String(), "warning: release v0.4.0 is not on the release feed and remote khz did not serve it "+
			"(the session for remote khz is not valid); running skali v0.5.0")
	})
	t.Run("unreachable cluster and missing release", func(t *testing.T) {
		f, target := fetchFixture(t, deadURL(t))
		f.d.releaseBase, f.d.feedClient = feed.URL, feed.Client()
		_, err := f.d.ensureCLI(context.Background(), loadConfig(t), target, "v0.4.0")
		require.ErrorIs(t, err, errSilentFetch)
	})
}

func TestEnsureCLIAdoptsMovedClusterVersion(t *testing.T) {
	// The record says v0.4.0 but the cluster already runs v0.4.1: that is
	// what gets fetched, cached, and recorded.
	binary := fakeCLI("v0.4.1")
	cluster := newFakeCluster(t, "v0.4.1", binary)
	f, target := fetchFixture(t, cluster.URL)
	cfg := loadConfig(t)
	target = &remoteTarget{Name: "khz", Remote: cfg.Remotes["khz"]}

	fetch, err := f.d.ensureCLI(context.Background(), cfg, target, "v0.4.0")
	require.NoError(t, err)
	require.Equal(t, "v0.4.1", fetch.version)
	require.Equal(t, installer.CLICachePath(f.cache, "v0.4.1"), fetch.path)
	assertCached(t, f.cache, "v0.4.1", binary)
	require.Equal(t, "v0.4.1", loadConfig(t).Remotes["khz"].Version)
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
