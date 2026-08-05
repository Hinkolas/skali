package layout

//go:generate go run ../../cmd/skali-schema --schema layout --output ../../schemas/skali-layout.schema.json

import (
	"github.com/Hinkolas/skali/internal/utils"
	"github.com/Hinkolas/skali/internal/yamldoc"
	"github.com/google/jsonschema-go/jsonschema"
)

const SchemaID = "https://skali.dev/schemas/v1/skali-layout.schema.json"

func Schema() (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[Layout](&jsonschema.ForOptions{})
	if err != nil {
		return nil, err
	}

	yamldoc.StampSchema(schema, SchemaID, "Skali cluster layout",
		"Installation layout consumed by skali cluster: hosts, K3s roles, and designated node capabilities.")
	schema.Properties["version"].Const = new(any(CurrentVersion))
	schema.Properties["name"].Pattern = stableKeyPattern.String()
	schema.Properties["nodes"].PropertyNames = &jsonschema.Schema{
		Type:    "string",
		Pattern: stableKeyPattern.String(),
	}
	ApplyNodeGrammar(schema.Properties["nodes"].AdditionalProperties)

	return schema, nil
}

// ApplyNodeGrammar stamps the shared node grammar, the role enum and the
// capability set, onto a schema carrying role and capabilities
// properties. layout.yaml and the installer's node.yaml must agree on
// it, so both build their schemas through this one function.
func ApplyNodeGrammar(node *jsonschema.Schema) {
	node.Properties["role"].Enum = utils.AnySlice(RoleServer, RoleAgent)
	capabilities := node.Properties["capabilities"]
	capabilities.Items = &jsonschema.Schema{Type: "string", Enum: utils.AnySlice(Capabilities...)}
	capabilities.UniqueItems = true
}

func JSONSchema() ([]byte, error) {
	return yamldoc.MarshalSchema(Schema)
}
