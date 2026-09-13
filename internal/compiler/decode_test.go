package compiler

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecodeDefinitionRoundTrip(t *testing.T) {
	t.Parallel()
	definition := ProjectDefinition{Schema: DefinitionSchema, Name: "demo"}
	data, err := json.Marshal(definition)
	require.NoError(t, err)
	decoded, err := DecodeDefinition(data)
	require.NoError(t, err)
	require.Equal(t, "demo", decoded.Name)
	require.Equal(t, DefinitionSchema, decoded.Schema)
}

// Definitions stored up to v0.1.0-rc.2 carry the manifest version "1"
// instead of a schema; their shape is schema 1 and they decode forever.
func TestDecodeDefinitionReadsLegacyEnvelope(t *testing.T) {
	t.Parallel()
	decoded, err := DecodeDefinition([]byte(`{"version":"1","name":"demo","applications":{"web":{"source":{"kind":"image","image":"example.invalid/web:1"}}}}`))
	require.NoError(t, err)
	require.Equal(t, "demo", decoded.Name)
	require.Equal(t, DefinitionSchema, decoded.Schema)
	require.Contains(t, decoded.Applications, "web")
}

func TestDecodeDefinitionRejectsForeignSchema(t *testing.T) {
	t.Parallel()
	for _, document := range []string{`{"schema":99,"name":"demo"}`, `{"version":"99","name":"demo"}`, `{"name":"demo"}`} {
		_, err := DecodeDefinition([]byte(document))
		var unsupported *UnsupportedDefinitionError
		require.ErrorAs(t, err, &unsupported, document)
		require.Equal(t, DefinitionSchema, unsupported.Want)
		require.Contains(t, err.Error(), "resubmit the manifest")
	}
	_, err := DecodeDefinition([]byte(`{"schema":99,"name":"demo"}`))
	var unsupported *UnsupportedDefinitionError
	require.ErrorAs(t, err, &unsupported)
	require.Equal(t, 99, unsupported.Got)
	require.Contains(t, err.Error(), "schema 99 is not supported")
}

// Documents written by older builds carry the current schema on a
// differently shaped body (image as an expression object); the shape
// failure must map to the same typed error, not a raw json error.
func TestDecodeDefinitionMapsShapeMismatchToTypedError(t *testing.T) {
	t.Parallel()
	document := `{
		"version": "1",
		"name": "demo",
		"applications": {
			"web": {"source": {"kind": "image", "image": {"parts": [{"kind": "literal", "value": "x"}]}}}
		}
	}`
	_, err := DecodeDefinition([]byte(document))
	var unsupported *UnsupportedDefinitionError
	require.ErrorAs(t, err, &unsupported)
	require.Equal(t, DefinitionSchema, unsupported.Got)
	require.Contains(t, err.Error(), "does not decode in this build")
}
