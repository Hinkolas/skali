package installer

//go:generate go run ../../cmd/skali-schema --schema node --output ../../schemas/skali-node.schema.json
//go:generate go run ../../cmd/skali-schema --schema init --output ../../schemas/skali-init.schema.json

import (
	"github.com/google/jsonschema-go/jsonschema"

	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/manifest"
	"github.com/Hinkolas/skali/internal/utils"
	"github.com/Hinkolas/skali/internal/yamldoc"
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
	yamldoc.StampSchema(schema, NodeSchemaID, "Skali installer node configuration",
		"Per-host install configuration consumed by skali cluster install --config.")
	layout.ApplyNodeGrammar(schema)
	schema.Properties["vm"].Properties["network"].Enum = utils.AnySlice("bridged", "shared", "user-v2")
	bind := schema.Properties["network"].Properties["coordinatorBind"]
	bind.Items = &jsonschema.Schema{
		Type: "string", Enum: utils.AnySlice(NetworkScopeCluster, NetworkScopePublic),
	}
	bind.UniqueItems = true
	return schema, nil
}

// InitConfigSchema describes init.yaml for editors.
func InitConfigSchema() (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[InitConfig](&jsonschema.ForOptions{})
	if err != nil {
		return nil, err
	}
	yamldoc.StampSchema(schema, InitSchemaID, "Skali installer init configuration",
		"Cluster initialization configuration consumed by skali cluster init --config.")
	preference := schema.Properties["platforms"].Properties["preference"]
	preference.Items = &jsonschema.Schema{
		Type: "string", Enum: utils.AnySlice(manifest.PlatformAMD64, manifest.PlatformARM64),
	}
	preference.UniqueItems = true
	return schema, nil
}

// NodeConfigJSONSchema renders the node schema for skali-schema.
func NodeConfigJSONSchema() ([]byte, error) {
	return yamldoc.MarshalSchema(NodeConfigSchema)
}

// InitConfigJSONSchema renders the init schema for skali-schema.
func InitConfigJSONSchema() ([]byte, error) {
	return yamldoc.MarshalSchema(InitConfigSchema)
}
