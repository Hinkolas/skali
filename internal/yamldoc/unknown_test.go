package yamldoc

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type leaf struct{ Value string }

func (l *leaf) UnmarshalYAML(node *yaml.Node) error {
	l.Value = node.Value
	return nil
}

type inner struct {
	Port int    `yaml:"port"`
	Tag  string `yaml:"tag,omitempty"`
}

type outer struct {
	Name     string           `yaml:"name"`
	Items    map[string]inner `yaml:"items"`
	List     []inner          `yaml:"list,omitempty"`
	Leaf     leaf             `yaml:"leaf,omitempty"`
	Untagged int
	Skipped  string `yaml:"-"`
}

// Unknown keys are reported with their dotted path and the key's position,
// through structs, maps, and sequences; custom unmarshalers are leaves.
func TestUnknownFieldsCarryPaths(t *testing.T) {
	t.Parallel()
	text := `name: demo
items:
  api:
    port: 1
    prot: tcp
list:
  - port: 2
    extra: true
leaf:
  anything: goes
untagged: 3
bogus: 1
`
	var out outer
	_, err := Parse([]byte(text), "doc.yml", "document", &out)
	require.Error(t, err)
	var diagnostics Diagnostics
	require.ErrorAs(t, err, &diagnostics)
	require.Len(t, diagnostics, 3)
	require.Equal(t, "items.api.prot", diagnostics[0].Path)
	require.Equal(t, 5, diagnostics[0].Line)
	require.Equal(t, 5, diagnostics[0].Column)
	require.Equal(t, UnknownFieldMessage, diagnostics[0].Message)
	require.Equal(t, "list[0].extra", diagnostics[1].Path)
	require.Equal(t, "bogus", diagnostics[2].Path)
	require.Equal(t, 12, diagnostics[2].Line)
	require.Equal(t, "doc.yml:5:5: items.api.prot: unknown field\ndoc.yml:8:5: list[0].extra: unknown field\ndoc.yml:12:1: bogus: unknown field", err.Error())
}

// A type mismatch is not an unknown field and keeps the parser's message.
func TestTypeMismatchKeepsSyntaxDiagnostic(t *testing.T) {
	t.Parallel()
	var out outer
	_, err := Parse([]byte("name: demo\nitems:\n  api:\n    port: eighty\n"), "doc.yml", "document", &out)
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot unmarshal")
}

func TestPathsListsEverythingWritten(t *testing.T) {
	t.Parallel()
	var out outer
	document, err := Parse([]byte("name: demo\nitems:\n  api:\n    port: 1\n"), "doc.yml", "document", &out)
	require.NoError(t, err)
	require.Equal(t, []string{"items", "items.api", "items.api.port", "name"}, document.Paths())
}
