package manifest

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/version"
	"github.com/Hinkolas/skali/internal/yamldoc"
)

func TestDefaultChangeReportsEveryOmittedParent(t *testing.T) {
	document, err := Parse([]byte("skali: v0.3.0\nname: demo\napplications:\n  first:\n    image: web:1\n  second:\n    image: web:1\n  explicit:\n    image: web:1\n    deployment:\n      rollout:\n        strategy: rolling\n"), "skali.yml")
	require.NoError(t, err)
	changes := []Change{{Release: "v0.4.0", Kind: ChangeChanged, Path: "applications.*.deployment.rollout.strategy", WhenOmitted: true, Message: "default changed", Hint: "review rollout"}}
	var diagnostics yamldoc.Diagnostics
	validateLedger(&diagnostics, document, changes, "v0.3.0")
	require.Len(t, diagnostics, 2)
	require.NotContains(t, diagnostics.Error(), "applications.explicit")
	require.Contains(t, diagnostics.Error(), "applications.first.deployment.rollout.strategy")
	require.Contains(t, diagnostics.Error(), "applications.second.deployment.rollout.strategy")
}

func TestWatermarkCanonicalizes(t *testing.T) {
	t.Parallel()
	for value, want := range map[string]string{
		"v0.1.0-rc.3": "v0.1.0-rc.3",
		"0.1.0-rc.3":  "v0.1.0-rc.3",
		" 1.2.3 ":     "v1.2.3",
	} {
		release, ok := Watermark(value)
		require.True(t, ok, value)
		require.Equal(t, want, release)
	}
	for _, value := range []string{"", "1", "v1", "v0.1.0-rc1", "v0.0.0-dev", "latest"} {
		_, ok := Watermark(value)
		require.False(t, ok, value)
	}
}

func TestReviewNote(t *testing.T) {
	t.Parallel()
	require.Empty(t, ReviewNote("v0.1.0-rc.3", "v0.1.0-rc.3"))
	require.Empty(t, ReviewNote("v0.1.0-rc.3", "v0.0.0-dev"))
	require.Empty(t, ReviewNote("nonsense", "v0.1.0-rc.3"))
	require.Equal(t, "reviewed against v0.1.0-rc.3; this CLI is v0.1.0-rc.4 and nothing this manifest uses changed since",
		ReviewNote("0.1.0-rc.3", "v0.1.0-rc.4"))
	require.Equal(t, "reviewed against v0.2.0, newer than this CLI (v0.1.0-rc.4)",
		ReviewNote("v0.2.0", "v0.1.0-rc.4"))
}

func TestReferenceReleaseFallsBackToTheLedger(t *testing.T) {
	withCLIVersion(t, "v0.0.0-dev")
	require.Equal(t, newestShipped(Ledger), ReferenceRelease())
	require.True(t, version.IsRelease(ReferenceRelease()), "a pending entry never becomes the reference")
	withCLIVersion(t, "v9.0.0")
	require.Equal(t, "v9.0.0", ReferenceRelease())
}

func parseValid(t *testing.T, text string) *Document {
	t.Helper()
	document, err := Parse([]byte(strings.TrimSpace(text)+"\n"), "skali.yml")
	require.NoError(t, err)
	return document
}

func TestValidateRequiresWatermark(t *testing.T) {
	t.Parallel()
	document := parseValid(t, `
name: demo
applications:
  web:
    image: example.invalid/web:1
`)
	diagnostics := Validate(document)
	require.Len(t, diagnostics, 1)
	require.Equal(t, "skali", diagnostics[0].Path)
	require.Contains(t, diagnostics[0].Message, "is required")
	require.Contains(t, diagnostics[0].Message, "for example skali: v")
}

func TestValidateRejectsNonRelease(t *testing.T) {
	t.Parallel()
	for _, value := range []string{`"1"`, "latest", "v0.1.0-rc1"} {
		document := parseValid(t, "skali: "+value+"\nname: demo\napplications:\n  web:\n    image: example.invalid/web:1\n")
		diagnostics := Validate(document)
		require.Len(t, diagnostics, 1, value)
		require.Equal(t, "skali", diagnostics[0].Path)
		require.Contains(t, diagnostics[0].Message, "is not a skali release")
	}
	document := parseValid(t, "skali: 0.1.0-rc.3\nname: demo\napplications:\n  web:\n    image: example.invalid/web:1\n")
	require.Empty(t, Validate(document), "the v prefix is optional")
}

// Shipped entries name releases oldest first; entries still waiting for
// their tag are written as Next and sit after every shipped one.
func TestLedgerEntriesAreReleasesInOrder(t *testing.T) {
	t.Parallel()
	require.NotEmpty(t, Ledger)
	require.False(t, Ledger[0].Pending(), "the first entry names the release the watermark started with")
	pending := false
	for i, change := range Ledger {
		require.NotEmpty(t, change.Path)
		require.NotEmpty(t, change.Message)
		require.NotEmpty(t, change.Hint)
		require.Contains(t, []ChangeKind{ChangeAdded, ChangeRemoved, ChangeChanged}, change.Kind)
		if change.Pending() {
			pending = true
			continue
		}
		require.False(t, pending, "shipped entry %d follows a pending one; pending entries go last", i)
		require.True(t, version.IsRelease(change.Release), "%d: %q", i, change.Release)
		if i > 0 {
			require.False(t, version.Older(change.Release, Ledger[i-1].Release), "ledger is not oldest first at %d", i)
		}
	}
	require.Len(t, ChangesSince("v0.1.0-rc.2"), len(Ledger))
	require.Empty(t, ChangesSince("v9.9.9"))
	require.Len(t, ChangesSince("not a release"), len(Ledger))
}

// A pending entry is newer than every release the ledger names: a
// watermark at or before the newest shipped entry has not seen it, one
// past it has. Messages call its release "the next release".
func TestPendingEntryFollowsTheNewestShippedRelease(t *testing.T) {
	t.Parallel()
	ledger := []Change{
		{Release: "v0.1.0-rc.3", Kind: ChangeRemoved, Path: "version", Message: "m", Hint: "h"},
		{Release: "v0.1.0-rc.7", Kind: ChangeAdded, Path: "applications.*.routes.*.compress", Message: "m", Hint: "h"},
		{Release: Next, Kind: ChangeRemoved, Path: "backups", Message: "gone", Hint: "drop it"},
	}
	require.Equal(t, "v0.1.0-rc.7", newestShipped(ledger))
	require.Equal(t, "the next release", ledger[2].ReleaseLabel())
	require.Equal(t, "v0.1.0-rc.7", ledger[1].ReleaseLabel())
	require.Len(t, changesSince(ledger, "v0.1.0-rc.2"), 3)
	require.Equal(t, []Change{ledger[2]}, changesSince(ledger, "v0.1.0-rc.7"))
	require.Empty(t, changesSince(ledger, "v0.1.0-rc.8"), "a watermark past every shipped entry has seen the pending change")
	require.Empty(t, changesSince(ledger, "v0.1.0"))
	require.True(t, Change{Release: Next}.after(nil, "v9.9.9"), "with nothing shipped a pending change is after everything")
	require.Equal(t, "", newestShipped(nil))

	changed := []Change{{Release: Next, Kind: ChangeChanged, Path: "applications.*.build.dockerfile", Message: "moved", Hint: "rewrite it"}}
	document := parseValid(t, "skali: v0.1.0-rc.7\nname: demo\napplications:\n  web:\n    build:\n      context: ./web\n      dockerfile: Dockerfile\n")
	var diagnostics yamldoc.Diagnostics
	validateLedger(&diagnostics, document, changed, "v0.1.0-rc.7")
	require.Len(t, diagnostics, 1)
	require.Contains(t, diagnostics[0].Message, "moved (changed in the next release; this manifest was reviewed against v0.1.0-rc.7)")
	diagnostics = nil
	validateLedger(&diagnostics, document, changed, "v0.1.0-rc.8")
	require.Len(t, diagnostics, 1, "with nothing shipped in this ledger the pending change is after every watermark")

	shipped := append([]Change{{Release: "v0.1.0-rc.7", Kind: ChangeAdded, Path: "x", Message: "m", Hint: "h"}}, changed...)
	diagnostics = nil
	validateLedger(&diagnostics, document, shipped, "v0.1.0-rc.8")
	require.Empty(t, diagnostics, "a watermark past the newest shipped entry acknowledges the pending change")
}

func TestChangeMatchesPaths(t *testing.T) {
	t.Parallel()
	change := Change{Path: "applications.*.build.dockerfile"}
	require.True(t, change.Matches("applications.web.build.dockerfile"))
	require.False(t, change.Matches("applications.web.build"))
	require.False(t, change.Matches("applications.web.build.dockerfile.more"))
	require.False(t, change.Matches("databases.web.build.dockerfile"))
	require.True(t, Change{Path: "version"}.Matches("version"))
	require.False(t, Change{Path: "version"}.Matches("databases.main.version"))
}

// A changed meaning errors while the watermark predates the change and
// passes once the author moves the watermark past it.
func TestChangedEntryErrorsUntilAcknowledged(t *testing.T) {
	t.Parallel()
	ledger := []Change{{
		Release: "v0.2.0", Kind: ChangeChanged, Path: "applications.*.build.dockerfile",
		Message: "dockerfile is resolved against the build context", Hint: "rewrite it relative to context",
	}}
	text := `
skali: %s
name: demo
applications:
  web:
    build:
      context: ./web
      dockerfile: Dockerfile
`
	document := parseValid(t, strings.TrimSpace(strings.ReplaceAll(text, "%s", "v0.1.0")))
	var diagnostics = Validate(document)
	require.Empty(t, diagnostics, "the real ledger has no changed entry")
	validateLedger(&diagnostics, document, ledger, "v0.1.0")
	require.Len(t, diagnostics, 1)
	require.Equal(t, "applications.web.build.dockerfile", diagnostics[0].Path)
	require.Equal(t, 7, diagnostics[0].Line)
	require.Equal(t, "dockerfile is resolved against the build context (changed in v0.2.0; this manifest was reviewed against v0.1.0); "+
		"rewrite it relative to context, then explicitly review and edit skali: to acknowledge", diagnostics[0].Message)

	diagnostics = nil
	validateLedger(&diagnostics, document, ledger, "v0.2.0")
	require.Empty(t, diagnostics)

	unaffected := parseValid(t, "skali: v0.1.0\nname: demo\napplications:\n  web:\n    image: example.invalid/web:1\n")
	diagnostics = nil
	validateLedger(&diagnostics, unaffected, ledger, "v0.1.0")
	require.Empty(t, diagnostics, "a manifest that does not write the path is not affected")
}
