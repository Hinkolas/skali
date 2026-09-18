package manifest

import (
	"fmt"
	"strings"

	"github.com/Hinkolas/skali/internal/version"
)

// The skali field is a watermark: the release the author last reviewed the
// manifest against (docs/versioning.md, decision 4). It is the
// acknowledgement token the change ledger reads, not an equality gate: a
// manifest is judged by what it uses, and the watermark only decides
// whether a changed meaning has been seen. It never enters the compiled
// definition, so moving it changes no hash and rolls nothing.

// Watermark canonicalizes an authored watermark to its release tag: the v
// is optional when writing and present when comparing. ok is false for
// anything that is not a release.
func Watermark(value string) (release string, ok bool) {
	release = strings.TrimSpace(value)
	if release != "" && !strings.HasPrefix(release, "v") {
		release = "v" + release
	}
	return release, version.IsRelease(release)
}

// ReferenceRelease is the release messages and skali manifest upgrade name
// as the current one: this binary's release, or on a development build the
// newest release a shipped ledger entry names (pending entries name none).
func ReferenceRelease() string {
	if version.IsRelease(version.Version) {
		return version.Version
	}
	return newestShipped(Ledger)
}

// ReviewNote is the informational line skali validate prints when the
// watermark and this CLI are different releases; empty otherwise. An older
// watermark is fine by construction: any change the manifest uses would
// have failed validation, so what remains is a review point in the past.
func ReviewNote(watermark, cli string) string {
	release, ok := Watermark(watermark)
	if !ok || !version.IsRelease(cli) || release == cli {
		return ""
	}
	if version.Older(release, cli) {
		return fmt.Sprintf("reviewed against %s; this CLI is %s and nothing this manifest uses changed since", release, cli)
	}
	return fmt.Sprintf("reviewed against %s, newer than this CLI (%s)", release, cli)
}
