package layout

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFixturesParseAndValidate(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		filepath.Join("testdata", "single-node.yml"),
		filepath.Join("testdata", "production.yml"),
	} {
		t.Run(strings.TrimSuffix(filepath.Base(path), ".yml"), func(t *testing.T) {
			t.Parallel()
			document, err := ParseFile(path)
			require.NoError(t, err)
			require.Empty(t, Validate(document))
		})
	}
}

func TestTopologyDerivation(t *testing.T) {
	t.Parallel()

	single, err := ParseFile(filepath.Join("testdata", "single-node.yml"))
	require.NoError(t, err)
	topology := single.Layout.Topology()
	require.Equal(t, 1, topology.Servers)
	require.Equal(t, 0, topology.Agents)
	require.Equal(t, TierSingle, topology.DatabaseTier)

	production, err := ParseFile(filepath.Join("testdata", "production.yml"))
	require.NoError(t, err)
	topology = production.Layout.Topology()
	require.Equal(t, 3, topology.Servers)
	require.Equal(t, 4, topology.Agents)
	require.Equal(t, 2, topology.Capable[CapabilityDatabase])
	require.Equal(t, 1, topology.Capable[CapabilityRegistry])
	require.Equal(t, TierAsynchronous, topology.DatabaseTier)
}

func TestDeriveTier(t *testing.T) {
	t.Parallel()
	require.Equal(t, TierSingle, DeriveTier(0))
	require.Equal(t, TierSingle, DeriveTier(1))
	require.Equal(t, TierAsynchronous, DeriveTier(2))
	require.Equal(t, TierSynchronous, DeriveTier(3))
	require.Equal(t, TierSynchronous, DeriveTier(5))
}

func TestStrictUnknownFieldIncludesSourceLine(t *testing.T) {
	t.Parallel()
	_, err := ParseFile(filepath.Join("testdata", "unknown-field.yml"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "line 6")
	require.Contains(t, err.Error(), "field capabilites not found")
}

func TestValidateRejects(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		source  string
		message string
	}{
		{
			name:    "no nodes",
			source:  "version: \"1\"\nname: demo\n",
			message: "must declare at least one node",
		},
		{
			name: "two servers",
			source: `
version: "1"
name: demo
nodes:
  a:
    role: server
    capabilities: [application, database, registry, edge]
  b:
    role: server
`,
			message: "requires exactly one server, or three servers",
		},
		{
			name: "unknown capability",
			source: `
version: "1"
name: demo
nodes:
  a:
    role: server
    capabilities: [application, database, registry, edge, gpu]
`,
			message: "unknown capability \"gpu\"",
		},
		{
			name: "duplicate capability",
			source: `
version: "1"
name: demo
nodes:
  a:
    role: server
    capabilities: [application, database, registry, edge, database]
`,
			message: "duplicate capability \"database\"",
		},
		{
			name: "agent without capabilities",
			source: `
version: "1"
name: demo
nodes:
  a:
    role: server
    capabilities: [application, database, registry, edge]
  b:
    role: agent
`,
			message: "an agent without capabilities cannot receive work",
		},
		{
			name: "missing required capability",
			source: `
version: "1"
name: demo
nodes:
  a:
    role: server
    capabilities: [application, database, edge]
`,
			message: "no node carries the registry capability",
		},
		{
			name: "invalid role",
			source: `
version: "1"
name: demo
nodes:
  a:
    role: master
    capabilities: [application, database, registry, edge]
`,
			message: "must be server or agent",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			document, err := Parse([]byte(strings.TrimLeft(testCase.source, "\n")), "layout.yml")
			require.NoError(t, err)
			diagnostics := Validate(document)
			require.NotEmpty(t, diagnostics)
			require.Contains(t, diagnostics.Error(), testCase.message)
		})
	}
}

func TestDiagnosticUsesSemanticPathAndLine(t *testing.T) {
	t.Parallel()
	document, err := Parse([]byte(strings.TrimSpace(`
version: "1"
name: demo
nodes:
  a:
    role: server
    capabilities: [application, database, registry, edge, gpu]
`)+"\n"), "layout.yml")
	require.NoError(t, err)
	diagnostics := Validate(document)
	require.NotEmpty(t, diagnostics)
	require.Equal(t, "nodes.a.capabilities[4]", diagnostics[0].Path)
	require.Equal(t, 6, diagnostics[0].Line)
}
