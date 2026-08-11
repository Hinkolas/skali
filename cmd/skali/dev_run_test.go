package main

import (
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

const devRunManifest = `version: "1"
name: rundemo
applications:
  web:
    image: example.invalid/web:1
    commands:
      seed: [bun, run, db:seed]
      shared: [bun, run, web:shared]
  api:
    image: example.invalid/api:1
    commands:
      migrate: [go, run, ./cmd/migrate]
      shared: [go, run, ./cmd/shared]
`

// parseArgsHarness mimics cobra's dash bookkeeping: args after -- land with
// ArgsLenAtDash set to the split point.
func parseArgsHarness(t *testing.T, arguments []string) (string, []string, error) {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, "skali.yml", devRunManifest)
	project, err := loadLocalProject(filepath.Join(root, "skali.yml"))
	require.NoError(t, err)

	var appKey string
	var argv []string
	command := &cobra.Command{
		Use:  "run",
		Args: cobra.ArbitraryArgs,
		RunE: func(command *cobra.Command, args []string) error {
			appKey, argv, err = parseDevRunArgs(command, args, project)
			return nil
		},
	}
	command.SetArgs(arguments)
	require.NoError(t, command.Execute())
	return appKey, argv, err
}

func TestParseDevRunArgs(t *testing.T) {
	t.Parallel()

	// Bare name, unique across applications.
	appKey, argv, err := parseArgsHarness(t, []string{"seed"})
	require.NoError(t, err)
	require.Equal(t, "web", appKey)
	require.Equal(t, []string{"bun", "run", "db:seed"}, argv)

	// App plus name.
	appKey, argv, err = parseArgsHarness(t, []string{"api", "migrate"})
	require.NoError(t, err)
	require.Equal(t, "api", appKey)
	require.Equal(t, []string{"go", "run", "./cmd/migrate"}, argv)

	// Ambiguous name lists the owners and the disambiguation form.
	_, _, err = parseArgsHarness(t, []string{"shared"})
	require.ErrorContains(t, err, "api and web")

	// Unknown name lists what exists; unknown app fails plainly.
	_, _, err = parseArgsHarness(t, []string{"ghost"})
	require.ErrorContains(t, err, "api migrate")
	_, _, err = parseArgsHarness(t, []string{"ghost", "seed"})
	require.ErrorContains(t, err, `unknown application "ghost"`)
	_, _, err = parseArgsHarness(t, []string{"web", "migrate"})
	require.ErrorContains(t, err, "declares no command")

	// Raw argv with an explicit app.
	appKey, argv, err = parseArgsHarness(t, []string{"web", "--", "sh", "-c", "exit 7"})
	require.NoError(t, err)
	require.Equal(t, "web", appKey)
	require.Equal(t, []string{"sh", "-c", "exit 7"}, argv)

	// No positionals and no argv is a usage error.
	_, _, err = parseArgsHarness(t, nil)
	require.ErrorContains(t, err, "usage:")
}
