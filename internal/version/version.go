// Package version carries the build version stamped at link time. It lives in
// its own package (not cmd/) so daemon internals — heartbeats, enrollment,
// the update scanner — can report and compare versions without importing a
// main package. It also names the published release artifacts a version
// resolves to, so the CLI, the coordinator, and skalid agree on one shape.
package version

import (
	"regexp"
	"strconv"
	"strings"
)

// Version is overridden by the release build:
//
//	go build -ldflags "-X github.com/Hinkolas/skali/internal/version.Version=v0.1.0"
var Version = "v0.0.0-dev"

const (
	// PublishedSkalidRepo prefixes every published control-plane image; a
	// release tags it with its own version.
	PublishedSkalidRepo = "ghcr.io/hinkolas/skalid:"
	// ReleaseRepo is the GitHub repository releases are published to; the
	// binaries and checksums.txt hang off its release download URLs.
	ReleaseRepo = "Hinkolas/skali"
)

// releasePattern matches versions a tagged release produces: vX.Y.Z,
// optionally with a dotted alpha, beta, or rc prerelease (v0.1.0-rc.1;
// never -rc1); only those have published images and binaries. The
// prerelease shape is deliberately narrow so git-describe dev versions
// (v0.1.0-3-gabc1234, -dirty) never match and keep resolving to the
// working tree. task release:tag enforces the same shape before a tag
// exists.
var releasePattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-(alpha|beta|rc)\.[0-9]+)?$`)

// IsRelease reports whether v names a tagged release.
func IsRelease(v string) bool {
	return releasePattern.MatchString(v)
}

// IsPrerelease reports whether v is a release with an alpha, beta, or rc
// suffix; false for plain releases and for anything that is not a release.
func IsPrerelease(v string) bool {
	return IsRelease(v) && strings.Contains(v, "-")
}

// PublishedSkalidImage is the control-plane image a release publishes.
func PublishedSkalidImage(v string) string { return PublishedSkalidRepo + v }

// PublishedSkalidVersion extracts the release version of a published skalid
// image reference; ok is false for anything else (working-tree builds,
// custom images), whose versions cannot be compared.
func PublishedSkalidVersion(image string) (string, bool) {
	tag, found := strings.CutPrefix(image, PublishedSkalidRepo)
	if !found || !IsRelease(tag) {
		return "", false
	}
	return tag, true
}

// ReleaseAssetURL is the download URL of one raw asset of a release, the
// same URL install.sh predicts: binaries are named <binary>_<os>_<arch>
// with no version in the name, and checksums.txt sits next to them.
func ReleaseAssetURL(base, v, asset string) string {
	return strings.TrimSuffix(base, "/") + "/" + ReleaseRepo + "/releases/download/" + v + "/" + asset
}

// DefaultReleaseBase is the GitHub host release assets download from.
const DefaultReleaseBase = "https://github.com"

// Older reports version a strictly older than b, tolerating a leading "v"
// and ordering alpha, beta, and rc prereleases before their release
// (v0.1.0-rc.1 < v0.1.0); false when either does not parse, so callers
// only act on drift they can actually judge.
func Older(a, b string) bool {
	got, okA := parseCore(a)
	want, okB := parseCore(b)
	if !okA || !okB {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return got[i] < want[i]
		}
	}
	preA, okA := parsePrerelease(a)
	preB, okB := parsePrerelease(b)
	if !okA || !okB {
		return false
	}
	// A release outranks every prerelease of the same version.
	if preA == nil || preB == nil {
		return preA != nil && preB == nil
	}
	for i := range preA {
		if preA[i] != preB[i] {
			return preA[i] < preB[i]
		}
	}
	return false
}

var prereleasePattern = regexp.MustCompile(`^(alpha|beta|rc)\.([0-9]+)$`)

// parsePrerelease returns the {stage, number} rank of the prerelease
// suffix after the first "-" (alpha < beta < rc), nil for a plain release,
// and ok=false for any other suffix, which is not comparable.
func parsePrerelease(s string) (rank []int, ok bool) {
	_, suffix, found := strings.Cut(strings.TrimSpace(s), "-")
	if !found {
		return nil, true
	}
	match := prereleasePattern.FindStringSubmatch(suffix)
	if match == nil {
		return nil, false
	}
	stage := map[string]int{"alpha": 0, "beta": 1, "rc": 2}[match[1]]
	number, err := strconv.Atoi(match[2])
	if err != nil {
		return nil, false
	}
	return []int{stage, number}, true
}

// parseCore reads major.minor.patch, stopping at the first segment without
// a leading number; ok requires at least the major.
func parseCore(s string) (parts [3]int, ok bool) {
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
