package localdev

// Managed k3d: skali dev owns its one hard dependency beyond docker the
// same way skali cluster owns Lima (internal/installer/limavm): a pinned
// release verified against the sha256 from its published checksums.txt,
// downloaded rootless into the managed bin directory, and only used when
// no k3d is on PATH. An existing k3d (brew or otherwise) always wins.

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const (
	// k3dPinnedVersion is installed into the managed bin directory when no
	// k3d is on PATH; a version bump must update k3dSHA256 in lockstep
	// from the release's checksums.txt.
	k3dPinnedVersion = "5.9.0"
	// k3dMinVersion is the oldest k3d the local platform is known to work
	// with; an older one on PATH is refused with an upgrade hint, never
	// shadowed.
	k3dMinVersion = "5.6.0"
)

var k3dSHA256 = map[string]string{
	"darwin/amd64": "b4aabc37534f95b9c764e7823f2df923f50d57600837aa60a06266cce47db732",
	"darwin/arm64": "fe106541d5d0a3f18debcd4d432a16f8c0ce3e6ddc06f8fbb6f696a122313e00",
	"linux/amd64":  "06d8f25bc3a971c4eb29e0ff08429b180402db0f4dec838c9eac427e296800a0",
	"linux/arm64":  "03cde5cf23e6e8e67de5a039ecf26e5b85aca82fba3e5d13dadf904cd218a250",
}

// errK3dMissing asks the caller to install the managed pin: no k3d is on
// PATH and the managed binary is absent or no longer matches the pin.
var errK3dMissing = errors.New("k3d is missing")

// Seams for tests; production resolves the PATH and home directly and
// downloads over net/http.
var (
	lookPath    = exec.LookPath
	userHomeDir = os.UserHomeDir
	httpGet     = func(ctx context.Context, url string) (io.ReadCloser, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return nil, err
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			return nil, fmt.Errorf("GET %s: %s", url, response.Status)
		}
		return response.Body, nil
	}
)

func k3dAssetURL() string {
	return fmt.Sprintf("https://github.com/k3d-io/k3d/releases/download/v%s/k3d-%s-%s",
		k3dPinnedVersion, runtime.GOOS, runtime.GOARCH)
}

// k3dInstallHint is the manual fallback when the managed install cannot
// run (unsupported platform, no network).
func k3dInstallHint() string {
	if runtime.GOOS == "darwin" {
		return "brew install k3d, https://k3d.io"
	}
	return "https://k3d.io"
}

// managedBinDir is the rootless directory managed dependencies land in.
func managedBinDir() (string, error) {
	home, err := userHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".local", "share", "skali", "bin"), nil
}

func managedK3dPath() (string, error) {
	dir, err := managedBinDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "k3d"), nil
}

// k3dBinary resolves the k3d every lifecycle call execs: PATH first, the
// managed pinned binary second, and the bare name as a fallback so exec
// reports the usual not-found error.
func k3dBinary() string {
	if path, err := lookPath("k3d"); err == nil {
		return path
	}
	if managed, err := managedK3dPath(); err == nil {
		if info, err := os.Stat(managed); err == nil && info.Mode().IsRegular() {
			return managed
		}
	}
	return "k3d"
}

// checkK3d probes the resolved k3d and enforces the version floor. A k3d
// on PATH is the user's: too old or broken is their call to fix, with the
// fix named. The managed binary is ours: absent, broken, or off the pin
// all report errK3dMissing and Ensure (re)installs it.
func checkK3d(ctx context.Context) (string, error) {
	if path, err := lookPath("k3d"); err == nil {
		out, err := exec.CommandContext(ctx, path, "version").Output()
		if err != nil {
			return "", fmt.Errorf("k3d at %s is not working (k3d version failed): "+
				"reinstall it, or remove it and skali dev installs its own pinned copy", path)
		}
		version := k3dVersionString(string(out))
		if !versionAtLeast(version, k3dMinVersion) {
			return "", fmt.Errorf("k3d %s at %s is too old for skali dev: v%s or newer is required; "+
				"upgrade it, or remove it and skali dev installs its own pinned copy",
				version, path, k3dMinVersion)
		}
		if version == "" {
			version = "unknown"
		}
		return version, nil
	}
	path, err := managedK3dPath()
	if err != nil {
		return "", err
	}
	out, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil {
		return "", errK3dMissing
	}
	if version := k3dVersionString(string(out)); version == "v"+k3dPinnedVersion {
		return version, nil
	}
	// The managed binary tracks the pin exactly; anything else is
	// replaced on the next install.
	return "", errK3dMissing
}

// installK3d downloads the pinned release for this platform, verifies its
// checksum, lands it executable in the managed bin directory (the rename
// is atomic, so a torn download never becomes the managed binary), and
// smoke-probes it before reporting the path.
func installK3d(ctx context.Context) (string, error) {
	platform := runtime.GOOS + "/" + runtime.GOARCH
	wantSHA, ok := k3dSHA256[platform]
	if !ok {
		return "", fmt.Errorf("no pinned k3d build exists for %s", platform)
	}
	dir, err := managedBinDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	body, err := httpGet(ctx, k3dAssetURL())
	if err != nil {
		return "", err
	}
	defer body.Close()
	temp, err := os.CreateTemp(dir, "k3d-download-*")
	if err != nil {
		return "", err
	}
	hasher := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(temp, hasher), body)
	if err := cmp.Or(copyErr, temp.Close()); err != nil {
		os.Remove(temp.Name())
		return "", fmt.Errorf("download %s: %w", k3dAssetURL(), err)
	}
	if got := hex.EncodeToString(hasher.Sum(nil)); got != wantSHA {
		os.Remove(temp.Name())
		return "", fmt.Errorf("checksum mismatch for %s: got %s, want %s", k3dAssetURL(), got, wantSHA)
	}
	if err := os.Chmod(temp.Name(), 0o755); err != nil {
		os.Remove(temp.Name())
		return "", err
	}
	path := filepath.Join(dir, "k3d")
	if err := os.Rename(temp.Name(), path); err != nil {
		os.Remove(temp.Name())
		return "", err
	}
	if _, err := exec.CommandContext(ctx, path, "version").Output(); err != nil {
		return "", fmt.Errorf("the downloaded k3d does not run on this machine: %w", err)
	}
	return path, nil
}

// k3dVersionString extracts the "v5.9.0" token from k3d version output.
func k3dVersionString(output string) string {
	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		if version, found := strings.CutPrefix(line, "k3d version"); found {
			return strings.TrimSpace(version)
		}
	}
	return ""
}

// VersionOlder reports dotted version a strictly older than b, tolerating
// a leading "v"; false when either does not parse, so callers only act on
// drift they can actually judge.
func VersionOlder(a, b string) bool {
	got, okA := parseVersion(a)
	want, okB := parseVersion(b)
	if !okA || !okB {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return got[i] < want[i]
		}
	}
	return false
}

// versionAtLeast compares dotted numeric versions, tolerating a leading
// "v" and non-numeric suffixes ("v5.9.0-rc.1"). An unparseable version is
// accepted: refusing to run over a string we cannot judge would block dev
// builds of k3d.
func versionAtLeast(version, minimum string) bool {
	got, ok := parseVersion(version)
	if !ok {
		return true
	}
	want, _ := parseVersion(minimum)
	for i := range got {
		if got[i] != want[i] {
			return got[i] > want[i]
		}
	}
	return true
}

// parseVersion reads up to major.minor.patch, stopping at the first
// segment without a leading number; ok requires at least the major.
func parseVersion(s string) (parts [3]int, ok bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	for i, segment := range strings.SplitN(s, ".", 3) {
		digits := len(segment)
		for j, r := range segment {
			if r < '0' || r > '9' {
				digits = j
				break
			}
		}
		value, err := strconv.Atoi(segment[:digits])
		if err != nil {
			break
		}
		parts[i], ok = value, true
	}
	return parts, ok
}
