package installer

//go:generate go run ../../cmd/skali-schema --schema node --output ../../schemas/skali-node.schema.json
//go:generate go run ../../cmd/skali-schema --schema init --output ../../schemas/skali-init.schema.json

import (
	"encoding/json"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/Hinkolas/skali/internal/layout"
)

const (
	NodeSchemaID = "https://skali.dev/schemas/v1/skali-node.schema.json"
	InitSchemaID = "https://skali.dev/schemas/v1/skali-init.schema.json"
)

// NodeConfigSchema describes node.yaml for editors.
func NodeConfigSchema() (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[NodeConfig](&jsonschema.ForOptions{})
	if err != nil {
		return nil, err
	}
	schema.ID = NodeSchemaID
	schema.Schema = "https://json-schema.org/draft/2020-12/schema"
	schema.Title = "Skali installer node configuration"
	schema.Description = "Per-host install configuration consumed by skali-installer install --config."
	schema.Properties["role"].Enum = enum(layout.RoleServer, layout.RoleAgent)
	capabilities := schema.Properties["capabilities"]
	capabilities.Items = &jsonschema.Schema{Type: "string", Enum: enum(layout.Capabilities...)}
	capabilities.UniqueItems = true
	schema.Properties["vm"].Properties["network"].Enum = enum("bridged", "shared", "user-v2")
	return schema, nil
}

// InitConfigSchema describes init.yaml for editors.
func InitConfigSchema() (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[InitConfig](&jsonschema.ForOptions{})
	if err != nil {
		return nil, err
	}
	schema.ID = InitSchemaID
	schema.Schema = "https://json-schema.org/draft/2020-12/schema"
	schema.Title = "Skali installer init configuration"
	schema.Description = "Cluster initialization configuration consumed by skali-installer init --config."
	return schema, nil
}

// NodeConfigJSONSchema renders the node schema for skali-schema.
func NodeConfigJSONSchema() ([]byte, error) {
	return marshalSchema(NodeConfigSchema)
}

// InitConfigJSONSchema renders the init schema for skali-schema.
func InitConfigJSONSchema() ([]byte, error) {
	return marshalSchema(InitConfigSchema)
}

func marshalSchema(build func() (*jsonschema.Schema, error)) ([]byte, error) {
	schema, err := build()
	if err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func enum(values ...string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}
