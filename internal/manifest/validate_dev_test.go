package manifest

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func parseForValidation(t *testing.T, source string) *Document {
	t.Helper()
	document, err := Parse([]byte(strings.TrimSpace(source)+"\n"), "skali.yml")
	require.NoError(t, err)
	return document
}

func diagnosticPaths(document *Document) []string {
	var paths []string
	for _, diagnostic := range Validate(document) {
		paths = append(paths, diagnostic.Path)
	}
	return paths
}

func TestValidateCommandsAndDev(t *testing.T) {
	t.Parallel()

	valid := parseForValidation(t, `
version: "1"
name: demo
applications:
  web:
    image: example.invalid/web:1
    ports:
      web:
        port: 3000
    commands:
      seed: [bun, run, db:seed]
    dev:
      command: [bun, run, dev]
      ports:
        web: 5173
`)
	require.Empty(t, Validate(valid))

	cases := []struct {
		name   string
		source string
		path   string
	}{
		{
			name: "bad command key",
			source: `
version: "1"
name: demo
applications:
  web:
    image: example.invalid/web:1
    commands:
      Seed Data: [bun, run, db:seed]
`,
			path: "applications.web.commands.Seed Data",
		},
		{
			name: "empty command vector",
			source: `
version: "1"
name: demo
applications:
  web:
    image: example.invalid/web:1
    commands:
      seed: []
`,
			path: "applications.web.commands.seed",
		},
		{
			name: "dev without command",
			source: `
version: "1"
name: demo
applications:
  web:
    image: example.invalid/web:1
    dev:
      ports:
        web: 5173
`,
			path: "applications.web.dev.command",
		},
		{
			name: "dev port references unknown application port",
			source: `
version: "1"
name: demo
applications:
  web:
    image: example.invalid/web:1
    dev:
      command: [bun, run, dev]
      ports:
        web: 5173
`,
			path: "applications.web.dev.ports.web",
		},
		{
			name: "dev port out of range",
			source: `
version: "1"
name: demo
applications:
  web:
    image: example.invalid/web:1
    ports:
      web:
        port: 3000
    dev:
      command: [bun, run, dev]
      ports:
        web: 70000
`,
			path: "applications.web.dev.ports.web",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			document := parseForValidation(t, testCase.source)
			require.Contains(t, diagnosticPaths(document), testCase.path)
		})
	}
}
