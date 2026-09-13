package revision

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecodeRoundTrip(t *testing.T) {
	t.Parallel()
	document := Revision{Schema: Schema, Project: "demo", Environment: "production"}
	data, err := json.Marshal(document)
	require.NoError(t, err)
	decoded, err := Decode(data)
	require.NoError(t, err)
	require.Equal(t, "demo", decoded.Project)
	require.Equal(t, Schema, decoded.Schema)
}

// Revisions stored up to v0.1.0-rc.2 carry schemaVersion "1" instead of a
// schema; their shape is schema 1 and they decode forever.
func TestDecodeReadsLegacyEnvelope(t *testing.T) {
	t.Parallel()
	decoded, err := Decode([]byte(`{"schemaVersion":"1","project":"demo","environment":"production","definition":{"version":"1","name":"demo"}}`))
	require.NoError(t, err)
	require.Equal(t, "demo", decoded.Project)
	require.Equal(t, Schema, decoded.Schema)
}

func TestDecodeRejectsForeignSchema(t *testing.T) {
	t.Parallel()
	_, err := Decode([]byte(`{"schema":99,"project":"demo"}`))
	var schema *SchemaError
	require.ErrorAs(t, err, &schema)
	require.Equal(t, 99, schema.Got)
	require.Equal(t, Schema, schema.Want)
	require.Contains(t, err.Error(), "redeploy the environment")

	_, err = Decode([]byte(`{"schemaVersion":"99","project":"demo"}`))
	require.ErrorAs(t, err, &schema)
	require.Equal(t, 0, schema.Got)
}

// A document from another schema generation may not even fit the current
// struct shapes; the schema mismatch must win over the unmarshal failure.
func TestDecodeReportsSchemaBeforeShape(t *testing.T) {
	t.Parallel()
	document := `{
		"schema": 2,
		"project": "demo",
		"definition": {
			"applications": {
				"web": {"source": {"kind": "image", "image": {"parts": [{"kind": "literal", "value": "x"}]}}}
			}
		}
	}`
	_, err := Decode([]byte(document))
	var schema *SchemaError
	require.ErrorAs(t, err, &schema)
	require.Equal(t, 2, schema.Got)
}

func TestDecodeMissingSchemaIsSchemaError(t *testing.T) {
	t.Parallel()
	_, err := Decode([]byte(`{}`))
	var schema *SchemaError
	require.ErrorAs(t, err, &schema)
	require.Equal(t, 0, schema.Got)
}
