package compiler

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/manifest"
)

// The strict parser applies to JSON exactly as it does to YAML: unknown
// fields are rejected before compilation, so invalid input can never reach
// a target mutation regardless of syntax.
func TestJSONUnknownFieldRejected(t *testing.T) {
	t.Parallel()
	_, err := manifest.Parse([]byte(`{"version":"1","name":"demo","unknownfield":true}`), "skali.json")
	require.Error(t, err)
	require.ErrorContains(t, err, "unknownfield")
}

// Map order is syntax, not meaning: reordering YAML maps must not change
// the canonical hash.
func TestMapOrderDoesNotChangeHash(t *testing.T) {
	t.Parallel()
	ordered := "version: \"1\"\nname: demo\napplications:\n  api:\n    image: example.invalid/api:1\n  worker:\n    image: example.invalid/worker:1\n"
	reordered := "version: \"1\"\nname: demo\napplications:\n  worker:\n    image: example.invalid/worker:1\n  api:\n    image: example.invalid/api:1\n"

	first, err := manifest.Parse([]byte(ordered), "skali.yml")
	require.NoError(t, err)
	second, err := manifest.Parse([]byte(reordered), "skali.yml")
	require.NoError(t, err)
	firstResult, err := Compile(first)
	require.NoError(t, err)
	secondResult, err := Compile(second)
	require.NoError(t, err)
	require.Equal(t, firstResult.Hash, secondResult.Hash)
}
