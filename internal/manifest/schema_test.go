package manifest

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestGeneratedSchemaIsCurrent(t *testing.T) {
	t.Parallel()
	generated, err := JSONSchema()
	require.NoError(t, err)
	checkedIn, err := os.ReadFile(filepath.Join("..", "..", "schemas", "skali.schema.json"))
	require.NoError(t, err)
	require.True(t, bytes.Equal(checkedIn, generated), "run go generate ./internal/manifest")
}

func TestSchemaAcceptsExamples(t *testing.T) {
	t.Parallel()
	schema, err := Schema()
	require.NoError(t, err)
	resolved, err := schema.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true})
	require.NoError(t, err)
	for _, path := range []string{
		filepath.Join("..", "..", "examples", "hello-world", "skali.yml"),
		filepath.Join("..", "..", "examples", "file-sharing", "skali.yml"),
	} {
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		var value any
		require.NoError(t, yaml.Unmarshal(data, &value))
		require.NoError(t, resolved.Validate(value), path)
	}
}
