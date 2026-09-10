package installer

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/version"
)

// ReleaseBase is the host release assets download from. It is a variable
// only so the hostd daemons can honor SKALI_RELEASE_BASE from their
// environment file and the cluster e2e can serve a fake release from the
// test host; production always downloads from GitHub. No URL ever travels
// in a coordinator action: the agent derives every download from this base
// and the version it was handed.
var ReleaseBase = version.DefaultReleaseBase

// HostdEnvironmentPath is the optional root-owned environment file both
// hostd units read (systemd EnvironmentFile); it exists for SKALI_RELEASE_BASE.
const HostdEnvironmentPath = "/etc/skali/hostd.env"

// maxBinaryBytes bounds a binary download; the binaries are a few dozen
// megabytes, so a response larger than this is not a release asset.
const maxBinaryBytes = 256 << 20

// ReleaseMetadata is the release.json asset a release publishes next to
// its binaries: the k3s pin the release's installer carries, so the
// coordinator can judge the k3s move before any host changes and the
// console can name it.
type ReleaseMetadata struct {
	Version string `json:"version"`
	K3s     string `json:"k3s"`
}

// HostdAsset names the skali-hostd binary asset for an architecture, in
// goreleaser's <binary>_<os>_<arch> shape.
func HostdAsset(arch string) string {
	return "skali-hostd_linux_" + arch
}

// CLIAsset names the skali CLI binary asset for one platform, the asset
// install.sh downloads for the host it runs on.
func CLIAsset(goos, goarch string) string {
	return "skali_" + goos + "_" + goarch
}

// ReleaseChecksums reads a release's checksums.txt into asset name to hex
// sha256. It is the trust anchor every download is verified against, the
// same one install.sh uses.
func ReleaseChecksums(ctx context.Context, client *http.Client, base, release string) (map[string]string, error) {
	body, err := fetchReleaseAsset(ctx, client, base, release, "checksums.txt", 1<<20)
	if err != nil {
		return nil, err
	}
	sums := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		sums[strings.TrimPrefix(fields[1], "*")] = strings.ToLower(fields[0])
	}
	if len(sums) == 0 {
		return nil, fmt.Errorf("release %s publishes an empty checksums.txt", release)
	}
	return sums, nil
}

// FetchReleaseMetadata reads a release's release.json; a release without
// one (published before the file existed) yields nil, not an error.
func FetchReleaseMetadata(ctx context.Context, client *http.Client, base, release string) (*ReleaseMetadata, error) {
	body, err := fetchReleaseAsset(ctx, client, base, release, "release.json", 64<<10)
	if errors.Is(err, ErrAssetMissing) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var metadata ReleaseMetadata
	if err := json.Unmarshal(body, &metadata); err != nil {
		return nil, fmt.Errorf("decode release.json for %s: %w", release, err)
	}
	return &metadata, nil
}

// DownloadHostd fetches the skali-hostd binary of a release for one
// architecture and verifies it against the expected checksum before
// returning it; nothing unverified is ever written to the host.
func DownloadHostd(ctx context.Context, client *http.Client, base, release, arch, expectedSHA256 string) ([]byte, error) {
	return DownloadAsset(ctx, client, base, release, HostdAsset(arch), expectedSHA256)
}

// DownloadAsset fetches one binary asset of a release and verifies it
// against the expected checksum (from ReleaseChecksums) before returning
// it. An empty expected checksum is refused rather than trusted: it means
// the release publishes no such asset.
func DownloadAsset(ctx context.Context, client *http.Client, base, release, asset, expectedSHA256 string) ([]byte, error) {
	if expectedSHA256 == "" {
		return nil, fmt.Errorf("%s download has no expected checksum", asset)
	}
	body, err := fetchReleaseAsset(ctx, client, base, release, asset, maxBinaryBytes)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != strings.ToLower(expectedSHA256) {
		return nil, fmt.Errorf("%s for %s does not match its published checksum", asset, release)
	}
	return body, nil
}

// DefaultHostdCacheDir is where FetchHostd keeps verified downloads:
// $XDG_CACHE_HOME/skali/hostd, defaulting to ~/.cache/skali/hostd (the same
// XDG convention the CLI config uses; under sudo this is root's cache).
func DefaultHostdCacheDir() string {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		base = filepath.Join(os.Getenv("HOME"), ".cache")
	}
	return filepath.Join(base, "skali", "hostd")
}

// CachedHostd returns a hostd FetchHostd stored earlier for this release and
// architecture, re-verified against the digest recorded next to it so a
// damaged cache entry is refetched rather than installed. ok is false when
// there is no usable entry.
func CachedHostd(cacheDir, release, arch string) (data []byte, ok bool) {
	path := filepath.Join(cacheDir, release, HostdAsset(arch))
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	recorded, err := os.ReadFile(path + ".sha256")
	if err != nil {
		return nil, false
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != strings.TrimSpace(string(recorded)) {
		return nil, false
	}
	return data, true
}

// FetchHostd downloads a release's skali-hostd for one architecture,
// verified against the release's checksums.txt, and records it in the cache
// for CachedHostd. Caching is best effort: a cache that cannot be written
// does not fail the fetch. The CLI calls this when a cluster command needs
// the host daemon, so a laptop that never runs one never carries it.
func FetchHostd(ctx context.Context, client *http.Client, base, release, arch, cacheDir string) ([]byte, error) {
	asset := HostdAsset(arch)
	sums, err := ReleaseChecksums(ctx, client, base, release)
	if err != nil {
		if errors.Is(err, ErrAssetMissing) {
			return nil, fmt.Errorf("release %s was not found", release)
		}
		return nil, err
	}
	if sums[asset] == "" {
		return nil, fmt.Errorf("release %s publishes no %s", release, asset)
	}
	data, err := DownloadAsset(ctx, client, base, release, asset, sums[asset])
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(cacheDir, release)
	if err := os.MkdirAll(dir, 0o755); err == nil {
		path := filepath.Join(dir, asset)
		if writeFileAtomic(path, data, 0o755) == nil {
			_ = writeFileAtomic(path+".sha256", []byte(sums[asset]+"\n"), 0o644)
		}
	}
	return data, nil
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	name := temp.Name()
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		os.Remove(name)
		return err
	}
	if err := temp.Chmod(perm); err != nil {
		temp.Close()
		os.Remove(name)
		return err
	}
	if err := temp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// ErrAssetMissing marks a 404 for a release asset: the release does not
// exist, or it publishes no asset by that name. Callers match it with
// errors.Is to phrase the failure for their user.
var ErrAssetMissing = errors.New("release asset is missing")

func fetchReleaseAsset(ctx context.Context, client *http.Client, base, release, asset string, limit int64) ([]byte, error) {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	url := version.ReleaseAssetURL(base, release, asset)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "skali-hostd/"+version.Version)
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: %s", ErrAssetMissing, url)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: HTTP %d", url, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("download %s: response exceeds %d bytes", url, limit)
	}
	return body, nil
}

// ReplaceHostdAndRestart swaps the hostd binary in place (a rename, so the
// running process keeps its old inode) and schedules both units to restart
// a moment later from a transient systemd unit: the calling agent is about
// to be killed by that restart, so the restart cannot be a child of it.
// The coordinator unit restarts only where it exists (servers); on agents
// systemctl reports it as not loaded and the restart of the agent unit still
// proceeds because the units are restarted separately.
func ReplaceHostdAndRestart(ctx context.Context, runner host.Runner, binary []byte, coordinator bool) error {
	if len(binary) == 0 {
		return errors.New("skali-hostd binary is empty")
	}
	if err := runner.ReplaceFile(ctx, HostdBinaryPath, HostdBinaryPath+".prev", binary, 0o755); err != nil {
		return fmt.Errorf("install %s: %w", HostdBinaryPath, err)
	}
	command := "systemctl restart " + HostdAgentUnit
	if coordinator {
		command = "systemctl restart " + HostdCoordinatorUnit + "; " + command
	}
	result, err := runner.Run(ctx, host.Command{
		Name: "systemd-run",
		Args: []string{"--unit=skali-hostd-upgrade", "--on-active=2s",
			"/bin/sh", "-c", command},
	})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("schedule hostd restart: exit %d: %s",
			result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return nil
}

// CheckK3sMove judges moving a host from the installed k3s to the pinned
// one with the rules PlanUpgrade applies: no downgrade (k3s cannot run an
// older binary over a newer datastore) and at most one Kubernetes minor at
// a time. Unparseable versions pass, matching PlanUpgrade, which skips
// ordering guards it cannot judge rather than refusing.
func CheckK3sMove(installed, pinned string) error {
	downgrade, minorSkip := classifyK3sMove(installed, pinned)
	if downgrade {
		return fmt.Errorf("k3s %s is installed but the release pins %s; a downgrade is not supported",
			installed, pinned)
	}
	if minorSkip {
		return fmt.Errorf("k3s %s is installed but the release pins %s; in-place upgrades cross at most one Kubernetes minor",
			installed, pinned)
	}
	return nil
}

func classifyK3sMove(installed, pinned string) (downgrade, minorSkip bool) {
	cmp, ok := compareK3sVersions(installed, pinned)
	if !ok {
		return false, false
	}
	if cmp > 0 {
		return true, false
	}
	if cmp < 0 {
		from, _ := parseK3sVersion(installed)
		to, _ := parseK3sVersion(pinned)
		if to[0] != from[0] || to[1]-from[1] > 1 {
			return false, true
		}
	}
	return false, false
}
