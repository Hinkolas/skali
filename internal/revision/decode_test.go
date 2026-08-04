package revision

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecodeRoundTrip(t *testing.T) {
	t.Parallel()
	document := Revision{SchemaVersion: SchemaVersion, Project: "demo", Environment: "production"}
	data, err := json.Marshal(document)
	require.NoError(t, err)
	decoded, err := Decode(data)
	require.NoError(t, err)
	require.Equal(t, "demo", decoded.Project)
}

func TestDecodeRejectsForeignSchema(t *testing.T) {
	t.Parallel()
	_, err := Decode([]byte(`{"schemaVersion":"99","project":"demo"}`))
	var schema *SchemaError
	require.ErrorAs(t, err, &schema)
	require.Equal(t, "99", schema.Got)
	require.Equal(t, SchemaVersion, schema.Want)
	require.Contains(t, err.Error(), "redeploy the environment")
}

// A document from another schema generation may not even fit the current
// struct shapes (the v2 era stored image as an expression object); the
// version mismatch must win over the unmarshal failure.
func TestDecodeReportsSchemaBeforeShape(t *testing.T) {
	t.Parallel()
	document := `{
		"schemaVersion": "2",
		"project": "demo",
		"definition": {
			"version": "1",
			"applications": {
				"web": {"source": {"kind": "image", "image": {"parts": [{"kind": "literal", "value": "x"}]}}}
			}
		}
	}`
	_, err := Decode([]byte(document))
	var schema *SchemaError
	require.ErrorAs(t, err, &schema)
	require.Equal(t, "2", schema.Got)
}

func TestDecodeMissingVersionIsSchemaError(t *testing.T) {
	t.Parallel()
	_, err := Decode([]byte(`{}`))
	var schema *SchemaError
	require.ErrorAs(t, err, &schema)
	require.Equal(t, "", schema.Got)
}
