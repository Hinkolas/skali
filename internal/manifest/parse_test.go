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
	require.Contains(t, err.Error(), "line 7")
	require.Contains(t, err.Error(), "field ressources not found")
}

func TestDiscoverRejectsAmbiguousDefaults(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "skali.yml"), []byte("version: \"1\"\nname: one\napplications: {}\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "skali.yaml"), []byte("version: \"1\"\nname: two\napplications: {}\n"), 0o600))

	_, err := Discover("", directory)
	require.ErrorContains(t, err, "both")
	require.ErrorContains(t, err, "--manifest")
}

func TestDiagnosticUsesSemanticPathAndLine(t *testing.T) {
	t.Parallel()
	document, err := Parse([]byte(strings.TrimSpace(`
version: "1"
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
