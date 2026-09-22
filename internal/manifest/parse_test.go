package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
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
	require.Contains(t, err.Error(), "unknown-field.yml:6:5: applications.api.ressources: unknown field")
}

func TestRemovedMetadataFields(t *testing.T) {
	for _, key := range []string{"skali", "version"} {
		_, err := Parse([]byte(key+": v9.9.9\nname: demo\nbuckets:\n  files: {}\n"), "skali.yml")
		require.ErrorContains(t, err, key+": removed in manifest revision")
		require.ErrorContains(t, err, "run skali manifest upgrade")
	}
}

func TestDiscoverRejectsAmbiguousDefaults(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "skali.yml"), []byte("name: one\napplications: {}\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "skali.yaml"), []byte("name: two\napplications: {}\n"), 0o600))

	_, err := Discover("", directory)
	require.ErrorContains(t, err, "both")
	require.ErrorContains(t, err, "--manifest")
}

func TestDiagnosticUsesSemanticPathAndLine(t *testing.T) {
	t.Parallel()
	document, err := Parse([]byte(strings.TrimSpace(`
name: demo
applications:
  API:
    image: example.invalid/api:1
`)+"\n"), "skali.yml")
	require.NoError(t, err)
	diagnostics := Validate(document)
	require.NotEmpty(t, diagnostics)
	require.Equal(t, 4, diagnostics[0].Line)
	require.Equal(t, "applications.API", diagnostics[0].Path)
}
