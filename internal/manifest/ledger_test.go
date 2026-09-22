package manifest

import (
	"github.com/Hinkolas/skali/internal/yamldoc"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"testing/fstest"
)

func parseValid(t *testing.T, text string) *Document {
	t.Helper()
	document, err := Parse([]byte(strings.TrimSpace(text)+"\n"), "skali.yml")
	require.NoError(t, err)
	return document
}

func TestDefaultChangeReportsEveryOmittedParent(t *testing.T) {
	document, err := Parse([]byte("name: demo\napplications:\n  first:\n    image: web:1\n  second:\n    image: web:1\n  explicit:\n    image: web:1\n    deployment:\n      rollout:\n        strategy: rolling\n"), "skali.yml")
	require.NoError(t, err)
	changes := []Change{{Revision: 4, Kind: ChangeChanged, Path: "applications.*.deployment.rollout.strategy", WhenOmitted: true, Message: "default changed", Hint: "review rollout"}}
	var diagnostics yamldoc.Diagnostics
	validateLedger(&diagnostics, document, changes, 3)
	require.Len(t, diagnostics, 2)
	require.NotContains(t, diagnostics.Error(), "applications.explicit")
	require.Contains(t, diagnostics.Error(), "applications.first.deployment.rollout.strategy")
	require.Contains(t, diagnostics.Error(), "applications.second.deployment.rollout.strategy")
}

func TestChangeMatchesPaths(t *testing.T) {
	t.Parallel()
	change := Change{Path: "applications.*.build.dockerfile"}
	require.True(t, change.Matches("applications.web.build.dockerfile"))
	require.False(t, change.Matches("applications.web.build"))
	require.False(t, change.Matches("applications.web.build.dockerfile.more"))
	require.False(t, change.Matches("databases.web.build.dockerfile"))
	require.True(t, Change{Path: "version"}.Matches("version"))
	require.False(t, Change{Path: "version"}.Matches("databases.main.version"))
}

func TestChangedEntryErrorsUntilAcknowledged(t *testing.T) {
	t.Parallel()
	ledger := []Change{{
		Revision: 2, Kind: ChangeChanged, Path: "applications.*.build.dockerfile",
		Message: "dockerfile is resolved against the build context", Hint: "rewrite it relative to context",
	}}
	text := `
name: demo
applications:
  web:
    build:
      context: ./web
      dockerfile: Dockerfile
`
	document := parseValid(t, text)
	var diagnostics = Validate(document)
	require.Empty(t, diagnostics, "the real ledger has no changed entry")
	validateLedger(&diagnostics, document, ledger, 1)
	require.Len(t, diagnostics, 1)
	require.Equal(t, "applications.web.build.dockerfile", diagnostics[0].Path)
	require.Equal(t, 6, diagnostics[0].Line)
	require.Contains(t, diagnostics[0].Message, "manifest revision 2; locally reviewed through 1")
	require.Contains(t, diagnostics[0].Message, "--acknowledge")

	diagnostics = nil
	validateLedger(&diagnostics, document, ledger, 2)
	require.Empty(t, diagnostics)

	unaffected := parseValid(t, "name: demo\napplications:\n  web:\n    image: example.invalid/web:1\n")
	diagnostics = nil
	validateLedger(&diagnostics, unaffected, ledger, 1)
	require.Empty(t, diagnostics, "a manifest that does not write the path is not affected")
}

func TestEmbeddedChanges(t *testing.T) {
	require.Equal(t, 5, CurrentRevision())
	require.Len(t, ChangesSince(0), 5)
	require.Empty(t, ChangesSince(5))
	require.Equal(t, "skali", ChangesSince(4)[0].Path)
}

func TestLoadChangesValidatesFiles(t *testing.T) {
	valid := "kind: changed\npath: applications.*.routes.*.compress\nmessage: changed\nhint: review\nwhenOmitted: true\n"
	fixture := func(files map[string]string) fstest.MapFS {
		fs := fstest.MapFS{}
		for name, data := range files {
			fs["changes/"+name] = &fstest.MapFile{Data: []byte(data)}
		}
		return fs
	}
	ledger, err := loadChanges(fixture(map[string]string{"00002_b.yaml": valid, "00001_a.yaml": valid}))
	require.NoError(t, err)
	require.Equal(t, 1, ledger[0].Revision)
	for name, files := range map[string]map[string]string{
		"gap":                {"00002_b.yaml": valid},
		"duplicate":          {"00001_a.yaml": valid, "00001_b.yaml": valid},
		"zero":               {"00000_a.yaml": valid},
		"filename":           {"1_a.yaml": valid},
		"unknown field":      {"00001_a.yaml": valid + "release: v1.0.0\n"},
		"malformed":          {"00001_a.yaml": "["},
		"multiple documents": {"00001_a.yaml": valid + "---\n" + valid},
		"empty path":         {"00001_a.yaml": strings.ReplaceAll(valid, "applications.*.routes.*.compress", "")},
		"partial wildcard":   {"00001_a.yaml": strings.ReplaceAll(valid, "routes", "routes*")},
		"kind":               {"00001_a.yaml": strings.ReplaceAll(valid, "kind: changed", "kind: surprise")},
		"missing hint":       {"00001_a.yaml": strings.ReplaceAll(valid, "hint: review", "hint: ''")},
		"omitted removal":    {"00001_a.yaml": strings.ReplaceAll(valid, "kind: changed", "kind: removed")},
	} {
		t.Run(name, func(t *testing.T) { _, err := loadChanges(fixture(files)); require.Error(t, err) })
	}
}
