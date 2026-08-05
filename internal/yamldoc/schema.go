package yamldoc

import (
	"encoding/json"

	"github.com/google/jsonschema-go/jsonschema"
)

// SchemaDraft is the JSON Schema dialect every generated schema declares.
const SchemaDraft = "https://json-schema.org/draft/2020-12/schema"

// StampSchema sets the identity header every generated schema carries.
func StampSchema(schema *jsonschema.Schema, id, title, description string) {
	schema.ID = id
	schema.Schema = SchemaDraft
	schema.Title = title
	schema.Description = description
}

// MarshalSchema renders a built schema in the generated-file convention:
// indented JSON with a trailing newline.
func MarshalSchema(build func() (*jsonschema.Schema, error)) ([]byte, error) {
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
