package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/updates"
	"github.com/Hinkolas/skali/internal/version"
)

// fakeUpgradeServer plays GitHub for one release tag: the releases
// listing under /releases and raw assets under the release download path.
// Every request path is recorded so tests can assert what was fetched.
type fakeUpgradeServer struct {
	*httptest.Server
	mu    sync.Mutex
	paths []string
}

func newFakeUpgradeServer(t *testing.T, tag string, assets map[string][]byte) *fakeUpgradeServer {
	t.Helper()
	fake := &fakeUpgradeServer{}
	mux := http.NewServeMux()
	record := func(r *http.Request) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		fake.paths = append(fake.paths, r.URL.Path)
	}
	mux.HandleFunc("/releases", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"tag_name":"` + tag + `","prerelease":` +
			strconv.FormatBool(version.IsPrerelease(tag)) +
			`,"published_at":"2026-09-01T00:00:00Z","html_url":"h/` + tag + `"}]`))
	})
	prefix := "/Hinkolas/skali/releases/download/" + tag + "/"
	mux.HandleFunc(prefix, func(w http.ResponseWriter, r *http.Request) {
		record(r)
		body, ok := assets[strings.TrimPrefix(r.URL.Path, prefix)]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	})
	fake.Server = httptest.NewServer(mux)
	t.Cleanup(fake.Close)
	return fake
}

func (f *fakeUpgradeServer) requested() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...)
}

func (f *fakeUpgradeServer) options(current string, channel updates.Channel, executable string) upgradeOptions {
	return upgradeOptions{
		Channel:     channel,
		Current:     current,
		Feed:        &updates.GitHubFeed{URL: f.URL + "/releases", Client: f.Client()},
		Client:      f.Client(),
		ReleaseBase: f.URL,
		Executable:  executable,
		GOOS:        runtime.GOOS,
		GOARCH:      runtime.GOARCH,
	}
}

func checksumEntry(name string, body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]) + "  " + name + "\n"
}

// fakeCLI is a stand-in release binary: a shell script that answers
// --version the way cobra does, so the post-install check runs for real.
func fakeCLI(version string) []byte {
	return []byte("#!/bin/sh\necho skali version " + version + "\n")
}

// releaseAssets builds a consistent release for the running platform.
func releaseAssets(cli, hostd []byte) map[string][]byte {
	cliAsset := installer.CLIAsset(runtime.GOOS, runtime.GOARCH)
	hostdAsset := installer.HostdAsset(runtime.GOARCH)
	return map[string][]byte{
		"checksums.txt": []byte(checksumEntry(cliAsset, cli) + checksumEntry(hostdAsset, hostd)),
		cliAsset:        cli,
		hostdAsset:      hostd,
	}
}

func writeExecutable(t *testing.T, dir, name string, body []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, body, 0o755))
	return path
}

func TestJudgeUpgrade(t *testing.T) {
	t.Parallel()
	release := func(v string) *updates.Release { return &updates.Release{Version: v} }
	cases := []struct {
		name      string
		current   string
		requested string
		latest    *updates.Release
		channel   updates.Channel
		target    string
		downgrade bool
		message   string
		err       string
	}{
		{name: "dev build refused without version", current: "v0.0.0-dev", channel: updates.ChannelStable,
			err: "--version"},
		{name: "dev build replaced on explicit version", current: "v0.1.0-3-gabc1234-dirty", requested: "v0.1.0",
			target: "v0.1.0"},
		{name: "explicit tag must be a release", current: "v0.1.0", requested: "latest",
			err: "exact release tag"},
		{name: "explicit equal is current", current: "v0.1.0", requested: "v0.1.0",
			message: "already current"},
		{name: "explicit older is a downgrade", current: "v0.2.0", requested: "v0.1.0-rc.1",
			target: "v0.1.0-rc.1", downgrade: true},
		{name: "explicit newer", current: "v0.1.0", requested: "v0.2.0", target: "v0.2.0"},
		{name: "empty stable channel hints beta", current: "v0.1.0-alpha.1", channel: updates.ChannelStable,
			message: "--channel beta"},
		{name: "empty beta channel", current: "v0.1.0-alpha.1", channel: updates.ChannelBeta,
			message: "no release is published yet"},
		{name: "channel equal is current", current: "v0.1.0", latest: release("v0.1.0"), channel: updates.ChannelStable,
			message: "already current"},
		{name: "prerelease ahead of stable hints beta", current: "v0.2.0-beta.1", latest: release("v0.1.0"),
			channel: updates.ChannelStable, message: "use --channel beta"},
		{name: "release ahead of beta says nothing to do", current: "v0.3.0", latest: release("v0.2.0-rc.1"),
			channel: updates.ChannelBeta, message: "nothing to do"},
		{name: "channel newer", current: "v0.1.0", latest: release("v0.1.1"), channel: updates.ChannelStable,
			target: "v0.1.1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome, err := judgeUpgrade(tc.current, tc.requested, tc.latest, tc.channel)
			if tc.err != "" {
				require.ErrorContains(t, err, tc.err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.target, outcome.Target)
			require.Equal(t, tc.downgrade, outcome.Downgrade)
			if tc.message != "" {
				require.Contains(t, outcome.Message, tc.message)
			} else {
				require.Empty(t, outcome.Message)
			}
		})
	}
}

func TestResolveUpgradeChannel(t *testing.T) {
	t.Parallel()
	channel, implied, err := resolveUpgradeChannel("", "v0.1.0")
	require.NoError(t, err)
	require.Equal(t, updates.ChannelStable, channel)
	require.False(t, implied)

	channel, implied, err = resolveUpgradeChannel("", "v0.1.0-alpha.3")
	require.NoError(t, err)
	require.Equal(t, updates.ChannelBeta, channel, "a prerelease build follows prereleases by default")
	require.True(t, implied)

	channel, implied, err = resolveUpgradeChannel("stable", "v0.1.0-alpha.3")
	require.NoError(t, err)
	require.Equal(t, updates.ChannelStable, channel, "an explicit flag wins")
	require.False(t, implied)

	_, _, err = resolveUpgradeChannel("nightly", "v0.1.0")
	require.ErrorContains(t, err, "unknown update channel")
}

func TestRunUpgradeReplacesCLIAndHostd(t *testing.T) {
	t.Parallel()
	cli, hostd := fakeCLI("v0.1.0-alpha.5"), []byte("hostd v0.1.0-alpha.5")
	server := newFakeUpgradeServer(t, "v0.1.0-alpha.5", releaseAssets(cli, hostd))
	dir := t.TempDir()
	executable := writeExecutable(t, dir, "skali", fakeCLI("v0.1.0-alpha.4"))
	installed := writeExecutable(t, dir, installer.HostdAsset(runtime.GOARCH), []byte("hostd old"))

	opts := server.options("v0.1.0-alpha.4", updates.ChannelBeta, executable)
	opts.ChannelImplied = true
	opts.Hostd = []string{installed, filepath.Join(dir, "absent", "skali-hostd"), installed}
	var out bytes.Buffer
	require.NoError(t, runUpgrade(context.Background(), &out, opts))

	got, err := os.ReadFile(executable)
	require.NoError(t, err)
	require.Equal(t, cli, got)
	info, err := os.Stat(executable)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o755), info.Mode().Perm())
	gotHostd, err := os.ReadFile(installed)
	require.NoError(t, err)
	require.Equal(t, hostd, gotHostd)

	text := out.String()
	require.Contains(t, text, "current  v0.1.0-alpha.4")
	require.Contains(t, text, "channel  beta (implied by prerelease build)")
	require.Contains(t, text, "target   v0.1.0-alpha.5")
	require.Contains(t, text, "upgraded skali v0.1.0-alpha.4 -> v0.1.0-alpha.5")
	require.Equal(t, 1, strings.Count(text, "Refresh "), "duplicate and absent candidates collapse")
	require.Contains(t, server.requested(), "/Hinkolas/skali/releases/download/v0.1.0-alpha.5/"+installer.HostdAsset(runtime.GOARCH))
}

func TestRunUpgradeSkipsAbsentHostd(t *testing.T) {
	t.Parallel()
	cli := fakeCLI("v0.2.0")
	server := newFakeUpgradeServer(t, "v0.2.0", releaseAssets(cli, []byte("hostd")))
	dir := t.TempDir()
	executable := writeExecutable(t, dir, "skali", fakeCLI("v0.1.0"))

	opts := server.options("v0.1.0", updates.ChannelStable, executable)
	opts.Hostd = []string{filepath.Join(dir, "skali-hostd")}
	var out bytes.Buffer
	require.NoError(t, runUpgrade(context.Background(), &out, opts))
	require.Contains(t, out.String(), "no installed skali-hostd to refresh")
	for _, path := range server.requested() {
		require.NotContains(t, path, "skali-hostd", "no hostd download without an installed hostd")
	}
}

func TestRunUpgradeExplicitDowngradeSkipsFeed(t *testing.T) {
	t.Parallel()
	cli := fakeCLI("v0.1.0")
	server := newFakeUpgradeServer(t, "v0.1.0", releaseAssets(cli, []byte("hostd")))
	dir := t.TempDir()
	executable := writeExecutable(t, dir, "skali", fakeCLI("v0.2.0"))

	opts := server.options("v0.2.0", updates.ChannelStable, executable)
	opts.Requested = "v0.1.0"
	var out bytes.Buffer
	require.NoError(t, runUpgrade(context.Background(), &out, opts))
	require.Contains(t, out.String(), "target   v0.1.0 (downgrade)")
	require.Contains(t, out.String(), "downgraded skali v0.2.0 -> v0.1.0")
	require.NotContains(t, out.String(), "channel", "an explicit version has no channel")
	require.NotContains(t, server.requested(), "/releases")
}

func TestRunUpgradeAlreadyCurrentDownloadsNothing(t *testing.T) {
	t.Parallel()
	server := newFakeUpgradeServer(t, "v0.1.0", releaseAssets(fakeCLI("v0.1.0"), []byte("hostd")))
	dir := t.TempDir()
	original := fakeCLI("v0.1.0")
	executable := writeExecutable(t, dir, "skali", original)

	var out bytes.Buffer
	require.NoError(t, runUpgrade(context.Background(), &out, server.options("v0.1.0", updates.ChannelStable, executable)))
	require.Contains(t, out.String(), "skali v0.1.0 is already current")
	require.Equal(t, []string{"/releases"}, server.requested())
	got, err := os.ReadFile(executable)
	require.NoError(t, err)
	require.Equal(t, original, got)
}

func TestRunUpgradeDevBuildRefusedBeforeNetwork(t *testing.T) {
	t.Parallel()
	server := newFakeUpgradeServer(t, "v0.1.0", nil)
	executable := writeExecutable(t, t.TempDir(), "skali", fakeCLI("v0.0.0-dev"))

	err := runUpgrade(context.Background(), &bytes.Buffer{}, server.options("v0.0.0-dev", updates.ChannelStable, executable))
	require.ErrorContains(t, err, "development build")
	require.ErrorContains(t, err, "--version")
	require.Empty(t, server.requested())
}

func TestRunUpgradeMissingReleaseAndAsset(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	executable := writeExecutable(t, dir, "skali", fakeCLI("v0.1.0"))

	server := newFakeUpgradeServer(t, "v0.9.0", nil)
	opts := server.options("v0.1.0", updates.ChannelStable, executable)
	opts.Requested = "v0.9.0"
	require.ErrorContains(t, runUpgrade(context.Background(), &bytes.Buffer{}, opts), "release v0.9.0 was not found")

	server = newFakeUpgradeServer(t, "v0.9.0", map[string][]byte{
		"checksums.txt": []byte(checksumEntry("skali_plan9_mips", []byte("x"))),
	})
	opts = server.options("v0.1.0", updates.ChannelStable, executable)
	opts.Requested = "v0.9.0"
	require.ErrorContains(t, runUpgrade(context.Background(), &bytes.Buffer{}, opts),
		"publishes no "+installer.CLIAsset(runtime.GOOS, runtime.GOARCH))
}

func TestRunUpgradeChecksumMismatchLeavesFileUntouched(t *testing.T) {
	t.Parallel()
	assets := releaseAssets(fakeCLI("v0.2.0"), []byte("hostd"))
	// The published digest names a different binary than the one served.
	assets["checksums.txt"] = []byte(checksumEntry(installer.CLIAsset(runtime.GOOS, runtime.GOARCH), []byte("tampered")))
	server := newFakeUpgradeServer(t, "v0.2.0", assets)
	dir := t.TempDir()
	original := fakeCLI("v0.1.0")
	executable := writeExecutable(t, dir, "skali", original)

	err := runUpgrade(context.Background(), &bytes.Buffer{}, server.options("v0.1.0", updates.ChannelStable, executable))
	require.ErrorContains(t, err, "does not match its published checksum")
	got, readErr := os.ReadFile(executable)
	require.NoError(t, readErr)
	require.Equal(t, original, got)
	entries, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	require.Len(t, entries, 1, "no temp files left behind")
}

func TestRunUpgradeUnwritableDirErrorsBeforeDownload(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	t.Parallel()
	server := newFakeUpgradeServer(t, "v0.2.0", nil)
	dir := t.TempDir()
	executable := writeExecutable(t, dir, "skali", fakeCLI("v0.1.0"))
	require.NoError(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	err := runUpgrade(context.Background(), &bytes.Buffer{}, server.options("v0.1.0", updates.ChannelStable, executable))
	require.ErrorContains(t, err, "requires root; run sudo skali upgrade")
	require.ErrorContains(t, err, dir)
	require.Empty(t, server.requested())
}

func TestRunUpgradeVerifyFailureRestoresPrevious(t *testing.T) {
	t.Parallel()
	// The release claims v0.2.0 but its binary answers with another version.
	server := newFakeUpgradeServer(t, "v0.2.0", releaseAssets(fakeCLI("v0.1.9"), []byte("hostd")))
	dir := t.TempDir()
	original := fakeCLI("v0.1.0")
	executable := writeExecutable(t, dir, "skali", original)
	installed := writeExecutable(t, dir, installer.HostdAsset(runtime.GOARCH), []byte("hostd old"))

	opts := server.options("v0.1.0", updates.ChannelStable, executable)
	opts.Hostd = []string{installed}
	err := runUpgrade(context.Background(), &bytes.Buffer{}, opts)
	require.ErrorContains(t, err, `reports "skali version v0.1.9", expected v0.2.0`)
	require.ErrorContains(t, err, "the previous binary was restored")
	got, readErr := os.ReadFile(executable)
	require.NoError(t, readErr)
	require.Equal(t, original, got)
	gotHostd, readErr := os.ReadFile(installed)
	require.NoError(t, readErr)
	require.Equal(t, []byte("hostd old"), gotHostd, "hostd is refreshed only after the CLI verified")
}

func TestRefreshableHostd(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	loose := writeExecutable(t, dir, "skali-hostd", []byte("a"))
	managed := writeExecutable(t, dir, "managed-hostd", []byte("b"))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "notafile"), 0o755))

	targets := refreshableHostd([]string{
		"", loose, filepath.Join(dir, "missing"), filepath.Join(dir, "notafile"), managed, loose + "/",
	}, managed, false)
	require.Equal(t, []hostdTarget{
		{Path: loose},
		{Path: managed, Skip: "managed by the cluster update"},
	}, targets)

	if os.Geteuid() != 0 {
		locked := t.TempDir()
		inside := writeExecutable(t, locked, "skali-hostd", []byte("c"))
		require.NoError(t, os.Chmod(locked, 0o555))
		t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
		targets = refreshableHostd([]string{inside}, "", false)
		require.Len(t, targets, 1)
		require.Contains(t, targets[0].Skip, "sudo skali upgrade")
	}
}

func TestHostdCandidatePaths(t *testing.T) {
	t.Parallel()
	asset := installer.HostdAsset(runtime.GOARCH)
	paths := hostdCandidatePaths("/opt/skali/bin/skali")
	require.Equal(t, []string{
		"/opt/skali/bin/skali-hostd",
		"/opt/skali/bin/" + asset,
		installer.HostdBinaryPath,
		filepath.Join(os.Getenv("HOME"), ".local", "share", "skali", asset),
	}, paths)
	require.Equal(t, paths[2:], hostdCandidatePaths(""), "no executable, no CLI-relative entries")
}
