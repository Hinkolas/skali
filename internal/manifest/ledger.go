package manifest

import (
	"strings"

	"github.com/Hinkolas/skali/internal/version"
)

// ChangeKind classifies one manifest grammar change for the ledger.
type ChangeKind string

const (
	// ChangeAdded is a new field or value. Silent: a manifest that does not
	// use it has nothing to learn. Recorded so the skill can list what
	// changed since a watermark.
	ChangeAdded ChangeKind = "added"
	// ChangeRemoved is a field that no longer exists. A manifest writing it
	// gets the entry's message and hint instead of a bare unknown field.
	ChangeRemoved ChangeKind = "removed"
	// ChangeChanged is a field whose meaning moved. A manifest writing it
	// fails until its watermark is at or past the release, which is how an
	// author acknowledges having read about the change.
	ChangeChanged ChangeKind = "changed"
)

// Change is one ledger entry: what changed, where, in which release, and
// what to do about it. Messages and hints are plain ASCII sentences.
type Change struct {
	Release string
	Kind    ChangeKind
	// Path is the dotted manifest path; * stands for one collection key
	// (applications.*.build.dockerfile).
	Path    string
	Message string
	Hint    string
	// WhenOmitted describes a default-only change: match missing fields,
	// including omitted parent objects, but not explicit values.
	WhenOmitted bool
}

// Ledger records every manifest grammar change since the watermark exists,
// oldest first (docs/versioning.md, decision 4). Parsing consults it for
// removed fields, validation for changed meanings, skali manifest upgrade
// moves watermarks past it, and the skill renders it. Changes older than
// the first entry predate every possible watermark and are history, not
// ledger.
var Ledger = []Change{
	{
		Release: "v0.1.0-rc.3",
		Kind:    ChangeRemoved,
		Path:    "version",
		Message: "version was replaced by skali, the release the manifest was last reviewed against",
		Hint:    "run skali manifest upgrade, or replace the line with skali: and that release, for example skali: v0.1.0-rc.3",
	},
	{
		Release: "v0.1.0-rc.5",
		Kind:    ChangeAdded,
		Path:    "backups.*.strategy",
		Message: "backup policies gained an optional strategy field; complete is the only value and the default, and policies are now enforced: snapshots run on the schedule and retention deletes the ones they produced",
		Hint:    "nothing to change; write strategy: complete to make the default explicit",
	},
	{
		Release: "v0.1.0-rc.7",
		Kind:    ChangeAdded,
		Path:    "applications.*.routes.*.compress",
		Message: "routes gained an optional compress field; the edge now compresses text-like responses by default (gzip, br, zstd) and compress: false opts a route out",
		Hint:    "nothing to change; write compress: false for routes that stream events or already compress their responses",
	},
	{
		Release: "v0.1.0-rc.8",
		Kind:    ChangeRemoved,
		Path:    "backups",
		Message: "backups was removed from the manifest; automatic backups are an environment setting now, one schedule per environment, set outside the manifest",
		Hint:    "run skali manifest upgrade to drop the block, then turn automatic backups on per environment with skali backup schedule set",
	},
}

// Matches reports whether a concrete manifest path is the one the change
// names: segment by segment, * standing for one collection key.
func (c Change) Matches(path string) bool {
	pattern := strings.Split(c.Path, ".")
	segments := strings.Split(path, ".")
	if len(pattern) != len(segments) {
		return false
	}
	for i := range pattern {
		if pattern[i] != "*" && pattern[i] != segments[i] {
			return false
		}
	}
	return true
}

// ChangesSince lists the entries that shipped after a watermark, oldest
// first; every entry when the watermark is not a release.
func ChangesSince(watermark string) []Change {
	release, ok := Watermark(watermark)
	var since []Change
	for _, change := range Ledger {
		if !ok || version.Older(release, change.Release) {
			since = append(since, change)
		}
	}
	return since
}

// Removed finds the removal entry a written path falls under.
func Removed(path string) (Change, bool) {
	return removedIn(Ledger, path)
}

func removedIn(ledger []Change, path string) (Change, bool) {
	for _, change := range ledger {
		if change.Kind == ChangeRemoved && change.Matches(path) {
			return change, true
		}
	}
	return Change{}, false
}
