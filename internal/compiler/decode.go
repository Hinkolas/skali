package compiler

import (
	"encoding/json"
	"fmt"
)

// legacyDefinitionVersion is the manifest version stamp stored definitions
// carried before DefinitionSchema existed (up to v0.1.0-rc.2). Their shape
// is DefinitionSchema 1 and they decode forever.
const legacyDefinitionVersion = "1"

// UnsupportedDefinitionError reports a stored definition document written
// under a schema generation this build does not decode. Callers may
// surface the message verbatim.
type UnsupportedDefinitionError struct {
	Got  int
	Want int
}

func (e *UnsupportedDefinitionError) Error() string {
	if e.Got == e.Want {
		return fmt.Sprintf("stored definition (schema %d) does not decode in this build; resubmit the manifest", e.Got)
	}
	return fmt.Sprintf("stored definition schema %d is not supported by this build (expected %d); resubmit the manifest", e.Got, e.Want)
}

// DecodeDefinition unmarshals a stored definition document of the current
// schema or of the legacy envelope. A body that carries a known schema but
// no longer fits the compiled shapes maps to the same typed error: the
// remedy (resubmit the manifest) is identical.
func DecodeDefinition(data []byte) (ProjectDefinition, error) {
	var peek struct {
		Schema  int    `json:"schema"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &peek); err != nil {
		return ProjectDefinition{}, fmt.Errorf("decode definition document: %w", err)
	}
	schema := peek.Schema
	if schema == 0 && peek.Version == legacyDefinitionVersion {
		schema = DefinitionSchema
	}
	if schema != DefinitionSchema {
		return ProjectDefinition{}, &UnsupportedDefinitionError{Got: schema, Want: DefinitionSchema}
	}
	var definition ProjectDefinition
	if err := json.Unmarshal(data, &definition); err != nil {
		return ProjectDefinition{}, &UnsupportedDefinitionError{Got: schema, Want: DefinitionSchema}
	}
	definition.Schema = DefinitionSchema
	return definition, nil
}
