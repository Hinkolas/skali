package compiler

import (
	"encoding/json"
	"fmt"

	"github.com/Hinkolas/skali/internal/manifest"
)

// UnsupportedDefinitionError reports a stored definition document written
// under a different schema generation than this build supports. Callers may
// surface the message verbatim.
type UnsupportedDefinitionError struct {
	Got  string
	Want string
}

func (e *UnsupportedDefinitionError) Error() string {
	return fmt.Sprintf("stored definition version %q is not supported by this build (expected %q); resubmit the manifest", e.Got, e.Want)
}

// DecodeDefinition unmarshals a stored definition document, accepting exactly
// the current manifest version. A body that carries the current version but
// no longer fits the compiled shapes still maps to the typed error: older
// builds stamped the same version string on differently shaped documents, and
// the remedy (resubmit the manifest) is identical.
func DecodeDefinition(data []byte) (ProjectDefinition, error) {
	var peek struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &peek); err != nil {
		return ProjectDefinition{}, fmt.Errorf("decode definition document: %w", err)
	}
	if peek.Version != manifest.CurrentVersion {
		return ProjectDefinition{}, &UnsupportedDefinitionError{Got: peek.Version, Want: manifest.CurrentVersion}
	}
	var definition ProjectDefinition
	if err := json.Unmarshal(data, &definition); err != nil {
		return ProjectDefinition{}, &UnsupportedDefinitionError{Got: peek.Version, Want: manifest.CurrentVersion}
	}
	return definition, nil
}
