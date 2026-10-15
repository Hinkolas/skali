package manifest

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGeneratedSchemaIsCurrent(t *testing.T) {
	t.Parallel()
	generated, err := JSONSchema()
	require.NoError(t, err)
	checkedIn, err := os.ReadFile(filepath.Join("..", "..", "schemas", "skali.schema.json"))
	require.NoError(t, err)
	require.True(t, bytes.Equal(checkedIn, generated), "run go generate ./internal/manifest")
}
