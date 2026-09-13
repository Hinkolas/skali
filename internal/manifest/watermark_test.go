package manifest

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/version"
)

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
	require.Equal(t, Ledger[len(Ledger)-1].Release, ReferenceRelease())
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

func TestLedgerEntriesAreReleasesInOrder(t *testing.T) {
	t.Parallel()
	require.NotEmpty(t, Ledger)
	for i, change := range Ledger {
		require.True(t, version.IsRelease(change.Release), "%d: %q", i, change.Release)
		require.NotEmpty(t, change.Path)
		require.NotEmpty(t, change.Message)
		require.NotEmpty(t, change.Hint)
		require.Contains(t, []ChangeKind{ChangeAdded, ChangeRemoved, ChangeChanged}, change.Kind)
		if i > 0 {
			require.False(t, version.Older(change.Release, Ledger[i-1].Release), "ledger is not oldest first at %d", i)
		}
	}
	require.Len(t, ChangesSince("v0.1.0-rc.2"), len(Ledger))
	require.Empty(t, ChangesSince("v9.9.9"))
	require.Len(t, ChangesSince("not a release"), len(Ledger))
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
		"rewrite it relative to context, then set skali: v0.2.0 or newer to acknowledge", diagnostics[0].Message)

	diagnostics = nil
	validateLedger(&diagnostics, document, ledger, "v0.2.0")
	require.Empty(t, diagnostics)

	unaffected := parseValid(t, "skali: v0.1.0\nname: demo\napplications:\n  web:\n    image: example.invalid/web:1\n")
	diagnostics = nil
	validateLedger(&diagnostics, unaffected, ledger, "v0.1.0")
	require.Empty(t, diagnostics, "a manifest that does not write the path is not affected")
}
