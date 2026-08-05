package layout

//go:generate go run ../../cmd/skali-schema --schema layout --output ../../schemas/skali-layout.schema.json

import (
	"encoding/json"

	"github.com/Hinkolas/skali/internal/utils"
	"github.com/google/jsonschema-go/jsonschema"
)

const SchemaID = "https://skali.dev/schemas/v1/skali-layout.schema.json"

func Schema() (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[Layout](&jsonschema.ForOptions{})
	if err != nil {
		return nil, err
	}

	schema.ID = SchemaID
	schema.Schema = "https://json-schema.org/draft/2020-12/schema"
	schema.Title = "Skali cluster layout"
	schema.Description = "Installation layout consumed by skali cluster: hosts, K3s roles, and designated node capabilities."
	schema.Properties["version"].Const = new(any(CurrentVersion))
	schema.Properties["name"].Pattern = stableKeyPattern.String()
	schema.Properties["nodes"].PropertyNames = &jsonschema.Schema{
		Type:    "string",
		Pattern: stableKeyPattern.String(),
	}

	node := schema.Properties["nodes"].AdditionalProperties
	node.Properties["role"].Enum = utils.AnySlice(RoleServer, RoleAgent)
	capabilities := node.Properties["capabilities"]
	capabilities.Items = &jsonschema.Schema{Type: "string", Enum: utils.AnySlice(Capabilities...)}
	capabilities.UniqueItems = true

	return schema, nil
}

func JSONSchema() ([]byte, error) {
	schema, err := Schema()
	if err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
