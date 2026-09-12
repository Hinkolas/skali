package main

import (
	"bytes"
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

func TestDevRunListTarget(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "skali.yml", devRunManifest)
	project, err := loadLocalProject(filepath.Join(root, "skali.yml"))
	require.NoError(t, err)

	target := func(arguments []string) (string, bool) {
		var app string
		var list bool
		command := &cobra.Command{
			Use:  "run",
			Args: cobra.ArbitraryArgs,
			RunE: func(command *cobra.Command, args []string) error {
				app, list = devRunListTarget(command, args, project)
				return nil
			},
		}
		command.SetArgs(arguments)
		require.NoError(t, command.Execute())
		return app, list
	}

	// Bare invocation lists everything; an application alone lists its own.
	app, list := target(nil)
	require.True(t, list)
	require.Empty(t, app)
	app, list = target([]string{"api"})
	require.True(t, list)
	require.Equal(t, "api", app)

	// Anything that names a command or carries argv runs instead.
	_, list = target([]string{"seed"})
	require.False(t, list)
	_, list = target([]string{"api", "migrate"})
	require.False(t, list)
	_, list = target([]string{"api", "--", "true"})
	require.False(t, list)
	_, list = target([]string{"--", "true"})
	require.False(t, list)
}

func TestRenderDevCommands(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "skali.yml", devRunManifest)
	project, err := loadLocalProject(filepath.Join(root, "skali.yml"))
	require.NoError(t, err)

	out := &bytes.Buffer{}
	renderDevCommands(out, project, "", "skali dev run")
	text := out.String()
	require.Contains(t, text, "commands  skali.yml")
	require.Regexp(t, `(?s)api\n  migrate  go run \./cmd/migrate\n  shared   go run \./cmd/shared\n\nweb\n  seed    bun run db:seed\n  shared  bun run web:shared\n`, text)
	require.Contains(t, text, "skali dev run <app> <name>")

	out.Reset()
	renderDevCommands(out, project, "web", "skali dev run")
	text = out.String()
	require.Contains(t, text, "web\n  seed")
	require.NotContains(t, text, "migrate")
	require.Contains(t, text, "run one with skali dev run web <name>")

	// No commands at all points at the manifest key and the raw form.
	writeFile(t, root, "bare.yml", "version: \"1\"\nname: bare\napplications:\n  web:\n    image: example.invalid/web:1\n")
	bare, err := loadLocalProject(filepath.Join(root, "bare.yml"))
	require.NoError(t, err)
	out.Reset()
	renderDevCommands(out, bare, "", "skali dev run")
	require.Contains(t, out.String(), "no commands declared in bare.yml")
	require.Contains(t, out.String(), "skali dev run [app] -- <command>...")
	out.Reset()
	renderDevCommands(out, bare, "web", "skali dev run")
	require.Contains(t, out.String(), "application web declares no commands")
	require.Contains(t, out.String(), "skali dev run web -- <command>...")
}

func TestShellWords(t *testing.T) {
	t.Parallel()
	require.Equal(t, "bun run db:seed", shellWords([]string{"bun", "run", "db:seed"}))
	require.Equal(t, `sh -c 'echo "hi there" && exit 1'`, shellWords([]string{"sh", "-c", `echo "hi there" && exit 1`}))
	require.Equal(t, `printf 'it'\''s'`, shellWords([]string{"printf", "it's"}))
	require.Equal(t, `touch ''`, shellWords([]string{"touch", ""}))
}
