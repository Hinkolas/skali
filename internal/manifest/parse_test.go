package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/version"
)

func TestExamplesParseAndValidate(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		filepath.Join("..", "..", "examples", "hello-world", "skali.yml"),
		filepath.Join("..", "..", "examples", "file-sharing", "skali.yml"),
		filepath.Join("..", "..", "examples", "dev-loop", "skali.yml"),
	} {
		path := path
		t.Run(filepath.Base(filepath.Dir(path)), func(t *testing.T) {
			t.Parallel()
			document, err := ParseFile(path)
			require.NoError(t, err)
			require.Empty(t, Validate(document))
		})
	}
}

func TestStrictUnknownFieldIncludesSourceLine(t *testing.T) {
	t.Parallel()
	_, err := ParseFile(filepath.Join("testdata", "unknown-field.yml"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown-field.yml:7:5: applications.api.ressources: unknown field")
}

// The legacy version field is the ledger's first removal: the parser names
// the replacement instead of a bare unknown field, in one line.
func TestLegacyVersionFieldNamesTheLedger(t *testing.T) {
	t.Parallel()
	_, err := Parse([]byte("version: \"1\"\nname: demo\napplications:\n  web:\n    image: example.invalid/web:1\n"), "skali.yml")
	require.Error(t, err)
	require.Equal(t, "skali.yml:1:1: version: removed in v0.1.0-rc.3: version was replaced by skali, "+
		"the release the manifest was last reviewed against; run skali manifest upgrade, "+
		"or replace the line with skali: and that release, for example skali: v0.1.0-rc.3", err.Error())
}

// A field this release has never seen is told apart from a typo when the
// watermark says the manifest targets a newer release.
func TestUnknownFieldNamesNewerWatermark(t *testing.T) {
	withCLIVersion(t, "v0.1.0-rc.3")
	_, err := Parse([]byte("skali: v9.9.9\nname: demo\napplications:\n  web:\n    image: example.invalid/web:1\n    sidecar: true\n"), "skali.yml")
	require.Error(t, err)
	require.Contains(t, err.Error(), "applications.web.sidecar: unknown field; the manifest was reviewed against v9.9.9, newer than this CLI (v0.1.0-rc.3)")

	_, err = Parse([]byte("skali: v0.1.0-rc.3\nname: demo\napplications:\n  web:\n    image: example.invalid/web:1\n    sidecar: true\n"), "skali.yml")
	require.Error(t, err)
	require.Contains(t, err.Error(), "applications.web.sidecar: unknown field")
	require.NotContains(t, err.Error(), "newer than")
}

// withCLIVersion swaps the linker-stamped version for one test; tests using
// it cannot run in parallel.
func withCLIVersion(t *testing.T, v string) {
	t.Helper()
	previous := version.Version
	version.Version = v
	t.Cleanup(func() { version.Version = previous })
}

func TestDiscoverRejectsAmbiguousDefaults(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "skali.yml"), []byte("skali: v0.1.0-rc.3\nname: one\napplications: {}\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "skali.yaml"), []byte("skali: v0.1.0-rc.3\nname: two\napplications: {}\n"), 0o600))

	_, err := Discover("", directory)
	require.ErrorContains(t, err, "both")
	require.ErrorContains(t, err, "--manifest")
}

func TestDiagnosticUsesSemanticPathAndLine(t *testing.T) {
	t.Parallel()
	document, err := Parse([]byte(strings.TrimSpace(`
skali: v0.1.0-rc.3
name: demo
applications:
  API:
    image: example.invalid/api:1
`)+"\n"), "skali.yml")
	require.NoError(t, err)
	diagnostics := Validate(document)
	require.NotEmpty(t, diagnostics)
	require.Equal(t, 5, diagnostics[0].Line)
	require.Equal(t, "applications.API", diagnostics[0].Path)
}
