package manifest

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/require"
)

// This is a frozen inventory from the commit that introduced the append-only
// ledger, not a golden to regenerate. New/removed keys must be accounted for by
// subsequent records. A record for a whole object covers its child properties.
// Meaning/default changes still require review; a field inventory cannot infer
// runtime semantics from a Go implementation.
func TestManifestFieldsHaveChangeRecords(t *testing.T) {
	var baseline struct {
		Revision int      `json:"revision"`
		Fields   []string `json:"fields"`
	}
	data, err := os.ReadFile("testdata/fields-at-revision-5.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &baseline))
	schema, err := Schema()
	require.NoError(t, err)
	current := map[string]bool{}
	var walk func(*jsonschema.Schema, string)
	walk = func(s *jsonschema.Schema, prefix string) {
		for key, child := range s.Properties {
			path := strings.TrimPrefix(prefix+"."+key, ".")
			current[path] = true
			walk(child, path)
		}
		if s.AdditionalProperties != nil {
			walk(s.AdditionalProperties, prefix+".*")
		}
		if s.Items != nil {
			walk(s.Items, prefix)
		}
	}
	walk(schema, "")
	old := map[string]bool{}
	for _, field := range baseline.Fields {
		old[field] = true
	}
	covered := func(field string, kind ChangeKind) bool {
		for _, change := range ChangesSince(baseline.Revision) {
			if change.Kind == kind && (field == change.Path || strings.HasPrefix(field, change.Path+".")) {
				return true
			}
		}
		return false
	}
	for field := range current {
		if !old[field] {
			require.True(t, covered(field, ChangeAdded), "%s needs an added record in changes/", field)
		}
	}
	for field := range old {
		if !current[field] {
			require.True(t, covered(field, ChangeRemoved), "%s needs a removed record in changes/", field)
		}
	}
}
