package compiler

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/manifest"
)

func TestDecodeDefinitionRoundTrip(t *testing.T) {
	t.Parallel()
	definition := ProjectDefinition{Version: manifest.CurrentVersion, Name: "demo"}
	data, err := json.Marshal(definition)
	require.NoError(t, err)
	decoded, err := DecodeDefinition(data)
	require.NoError(t, err)
	require.Equal(t, "demo", decoded.Name)
}

func TestDecodeDefinitionRejectsForeignVersion(t *testing.T) {
	t.Parallel()
	_, err := DecodeDefinition([]byte(`{"version":"99","name":"demo"}`))
	var unsupported *UnsupportedDefinitionError
	require.ErrorAs(t, err, &unsupported)
	require.Equal(t, "99", unsupported.Got)
	require.Contains(t, err.Error(), "resubmit the manifest")
}

// Documents written by older builds carry the current version string on a
// differently shaped body (image as an expression object); the shape failure
// must map to the same typed error, not a raw json error.
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
	require.Equal(t, "1", unsupported.Got)
}
