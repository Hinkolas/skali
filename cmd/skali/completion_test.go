package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/client"
)

// complete drives cobra's completion protocol the way a shell does and
// returns the offered values (value, tab, description) and the directive.
func complete(t *testing.T, args ...string) ([]string, string) {
	t.Helper()
	root := newRootCommand()
	root.SetArgs(append([]string{cobra.ShellCompRequestCmd}, args...))
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(io.Discard)
	require.NoError(t, root.ExecuteContext(context.Background()))
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	return lines[:len(lines)-1], lines[len(lines)-1]
}

const noFiles = ":4" // cobra.ShellCompDirectiveNoFileComp

func TestCompleteRemotesOffline(t *testing.T) {
	stageRemotes(t, "khz", map[string]*cliconfig.Remote{
		"khz":   {Master: "https://skali.khz.dev/api"},
		"lab":   {Master: "https://lab.example/api"},
		"local": {Master: "http://127.0.0.1:7070"},
	})
	values, directive := complete(t, "remote", "use", "")
	require.Equal(t, []string{"khz\thttps://skali.khz.dev/api", "lab\thttps://lab.example/api"}, values,
		"the dev-owned local remote is never a target")
	require.Equal(t, noFiles, directive)

	values, _ = complete(t, "deploy", "--remote", "l")
	require.Equal(t, []string{"lab\thttps://lab.example/api"}, values)
	values, _ = complete(t, "remote", "remove", "khz", "")
	require.Empty(t, values, "a second remote name is not an argument")
	values, _ = complete(t, "remote", "add", "")
	require.Empty(t, values, "a new remote's name is the user's to type")
}

func TestCompleteEnvironmentsAndProjects(t *testing.T) {
	seedBackupScope(t)
	values, directive := complete(t, "env", "remove", "")
	require.Equal(t, []string{"production\tadmin", "staging\tadmin"}, values)
	require.Equal(t, noFiles, directive)

	values, _ = complete(t, "deploy", "--environment", "st")
	require.Equal(t, []string{"staging\tadmin"}, values)
	values, _ = complete(t, "deploy", "--from", "")
	require.Len(t, values, 2)
	values, _ = complete(t, "env", "list", "--project", "")
	require.Equal(t, []string{"flowdemo\tadmin"}, values)

	// The comma list keeps what is typed and offers the rest; any only
	// stands alone.
	values, _ = complete(t, "env", "set", "--promote-from", "")
	require.Equal(t, []string{"any\tpromote from every environment", "production\tadmin", "staging\tadmin"}, values)
	values, _ = complete(t, "env", "set", "--promote-from", "production,")
	require.Equal(t, []string{"production,staging\tadmin"}, values)

	// Outside a checkout nothing resolves and nothing is offered.
	t.Chdir(t.TempDir())
	values, directive = complete(t, "env", "remove", "")
	require.Empty(t, values)
	require.Equal(t, noFiles, directive)
	values, _ = complete(t, "env", "remove", "--project", "flowdemo", "")
	require.Len(t, values, 2, "--project names the scope outside a checkout")
}

func TestCompleteSnapshotsRunsAndMembers(t *testing.T) {
	install := seedBackupScope(t)
	values, _ := complete(t, "backup", "restore", "")
	require.Len(t, values, 3)
	require.True(t, strings.HasPrefix(values[0], testSnapshotProduction+"\t"), values[0])
	require.Contains(t, values[0], "production  12.1KiB")
	values, _ = complete(t, "backup", "restore", testSnapshotStaging, "")
	require.Empty(t, values)

	production := "p1-e1"
	staging := "p1-e2"
	install.runs["run-a"] = client.Run{ID: "run-a", Kind: "deploy", Status: "running", EnvironmentID: &production}
	install.runs["run-b"] = client.Run{ID: "run-b", Kind: "backup", Status: "succeeded", EnvironmentID: &staging}
	install.steps["run-a"] = []client.Step{
		{Key: "artifacts", Title: "Build artifacts", Children: []client.Step{{Key: "artifacts.web.build", Title: "Build web"}}},
		{Key: "rollout", Title: "Roll out"},
	}
	values, _ = complete(t, "run", "cancel", "")
	require.Equal(t, []string{"run-a\tdeploy running"}, values, "the bound environment's runs")
	values, _ = complete(t, "run", "logs", "run-a", "--step", "")
	require.Equal(t, []string{"artifacts\tBuild artifacts", "artifacts.web.build\tBuild web", "rollout\tRoll out"}, values)
	values, _ = complete(t, "run", "logs", "--step", "")
	require.Empty(t, values, "no run named yet")

	install.revisions[production] = []client.RevisionSummary{
		{ID: "rev-1", Checksum: "sha256:abcdef1234567890", CreatedAt: time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)},
	}
	values, _ = complete(t, "rollback", "--revision", "")
	require.Len(t, values, 1)
	require.True(t, strings.HasPrefix(values[0], "rev-1\tabcdef123456  "), values[0])

	install.members["p1"] = []client.Member{
		{UserID: "u1", Email: "alice@example.com", Role: "admin"},
		{UserID: "u2", Email: "bob@example.com", Role: "read"},
	}
	values, _ = complete(t, "access", "set", "")
	require.Equal(t, []string{"alice@example.com\tadmin", "bob@example.com\tread"}, values)
	values, _ = complete(t, "access", "set", "bob@example.com", "")
	require.Equal(t, projectRoles, values)
	values, _ = complete(t, "access", "set", "--environment", "production", "bob@example.com", "")
	require.Equal(t, cellRoles, values)
	values, _ = complete(t, "access", "remove", "b")
	require.Equal(t, []string{"bob@example.com\tread"}, values)
}

const commandsManifest = `version: "1"
name: flowdemo
applications:
  web:
    build:
      context: .
    ports:
      http:
        port: 8080
        protocol: http
    commands:
      migrate: ["bun", "run", "migrate"]
      seed: ["bun", "run", "seed"]
  worker:
    build:
      context: .
    commands:
      seed: ["python", "seed.py", "--all"]
      lint: ["ruff", "check", "."]
`

func TestCompleteManifestNames(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "skali.yml", commandsManifest)
	t.Chdir(root)

	values, directive := complete(t, "dev", "run", "")
	require.Equal(t, []string{
		"web\tapplication", "worker\tapplication",
		"lint\tworker: ruff check .", "migrate\tweb: bun run migrate",
	}, values, "seed belongs to two applications and needs one named")
	require.Equal(t, noFiles, directive)
	values, _ = complete(t, "dev", "run", "worker", "")
	require.Equal(t, []string{"lint\truff check .", "seed\tpython seed.py --all"}, values)
	values, _ = complete(t, "dev", "run", "worker", "seed", "")
	require.Empty(t, values)
	_, directive = complete(t, "dev", "run", "--", "")
	require.Equal(t, ":0", directive, "after -- the shell completes the command itself")

	values, _ = complete(t, "logs", "")
	require.Equal(t, []string{"web", "worker"}, values)
	values, _ = complete(t, "exec", "w")
	require.Equal(t, []string{"web", "worker"}, values)
	values, _ = complete(t, "dev", "exec", "web", "")
	require.Empty(t, values)
	_, directive = complete(t, "exec", "web", "--", "")
	require.Equal(t, ":0", directive)

	// A manifest that does not parse completes nothing rather than failing.
	writeFile(t, root, "skali.yml", "version: [")
	values, directive = complete(t, "dev", "run", "")
	require.Empty(t, values)
	require.Equal(t, noFiles, directive)
}

func TestCompleteGivesUpOnSlowRemote(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		w.WriteHeader(http.StatusGatewayTimeout)
	}))
	t.Cleanup(slow.Close)
	stageRemotes(t, "slow", map[string]*cliconfig.Remote{"slow": {Master: slow.URL}})
	t.Chdir(t.TempDir())

	started := time.Now()
	values, directive := complete(t, "env", "remove", "--project", "flowdemo", "")
	require.Less(t, time.Since(started), 4*time.Second, "two seconds is the budget for a remote-backed completion")
	require.Empty(t, values)
	require.Equal(t, noFiles, directive)
}

// TestCompletionCoverage keeps the completion surface whole: every flag the
// table knows is completed wherever it is declared, every positional has a
// completer, and every command without positionals refuses file names.
func TestCompletionCoverage(t *testing.T) {
	root := newRootCommand()
	fileArguments := map[string]bool{"skali cluster changes import": true}
	var walk func(*cobra.Command)
	walk = func(command *cobra.Command) {
		path := command.CommandPath()
		for name := range flagCompleters {
			if command.LocalFlags().Lookup(name) == nil {
				continue
			}
			_, registered := command.GetFlagCompletionFunc(name)
			require.True(t, registered, "%s: --%s has no completer", path, name)
		}
		if fileArguments[path] {
			require.Nil(t, command.ValidArgsFunction, "%s: takes a file", path)
		} else {
			require.NotNil(t, command.ValidArgsFunction, "%s: no positional completer", path)
		}
		for _, child := range command.Commands() {
			walk(child)
		}
	}
	walk(root)

	values, _ := complete(t, "upgrade", "--channel", "")
	require.Equal(t, []string{"stable", "beta"}, values)
	values, _ = complete(t, "cluster", "node", "capabilities", "node-a", "edge", "")
	require.NotContains(t, values, "edge", "a capability already typed is not offered again")
	require.Contains(t, values, "application")
	_, directive := complete(t, "deploy", "--manifest", "")
	require.Equal(t, ":8", directive, "manifests filter to yaml files")
}

func TestCompletionScripts(t *testing.T) {
	for _, shell := range completionShells {
		root := newRootCommand()
		root.SetArgs([]string{"completion", shell})
		out := &bytes.Buffer{}
		root.SetOut(out)
		require.NoError(t, root.Execute(), shell)
		require.Contains(t, out.String(), "skali", shell)
	}
	root := newRootCommand()
	root.SetArgs([]string{"completion", "zsh"})
	out := &bytes.Buffer{}
	root.SetOut(out)
	require.NoError(t, root.Execute())
	require.True(t, strings.HasPrefix(out.String(), "#compdef skali\n"), "zsh autoloads by the #compdef line")
}

func TestInstallCompletionTargets(t *testing.T) {
	home := t.TempDir()
	installer := completionInstaller{
		home:          home,
		dataDir:       filepath.Join(home, ".local", "share"),
		configDir:     filepath.Join(home, ".config"),
		siteFunctions: []string{filepath.Join(home, "missing", "site-functions")},
	}
	root := newRootCommand()
	install := func(shell string) string {
		out := &bytes.Buffer{}
		require.NoError(t, installCompletion(out, root, shell, installer))
		return out.String()
	}

	// Without a site-functions directory zsh needs the fpath hint.
	out := install("zsh")
	zshPath := filepath.Join(home, ".local", "share", "zsh", "site-functions", "_skali")
	require.Contains(t, out, "installed zsh completions to ~/.local/share/zsh/site-functions/_skali")
	require.Contains(t, out, "fpath+=(~/.local/share/zsh/site-functions)")
	require.Contains(t, out, "restart your shell")
	script, err := os.ReadFile(zshPath)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(script), "#compdef skali\n"))

	// A writable site-functions directory takes the file and needs no hint.
	brew := filepath.Join(home, "brew", "site-functions")
	require.NoError(t, os.MkdirAll(brew, 0o755))
	installer.siteFunctions = []string{brew}
	out = install("zsh")
	require.Contains(t, out, "installed zsh completions to ~/brew/site-functions/_skali")
	require.NotContains(t, out, "fpath")
	require.Equal(t, []string{filepath.Join(brew, "_skali"), zshPath}, installer.installed("zsh"))

	out = install("bash")
	require.Contains(t, out, "~/.local/share/bash-completion/completions/skali")
	out = install("fish")
	require.Contains(t, out, "~/.config/fish/completions/skali.fish")
	require.Len(t, installer.installed("bash"), 1)
	require.Empty(t, completionInstaller{}.installed("powershell"))

	err = installCompletion(io.Discard, root, "powershell", installer)
	require.ErrorContains(t, err, "no install location")
	err = installCompletion(io.Discard, root, "tcsh", installer)
	require.ErrorContains(t, err, `unsupported shell "tcsh"`)

	env := map[string]string{"SHELL": "/bin/zsh"}
	getenv := func(key string) string { return env[key] }
	require.Equal(t, "zsh", loginShell(getenv))
	env["SKALI_SHELL"] = "fish"
	require.Equal(t, "fish", loginShell(getenv))
	require.Equal(t, "", loginShell(func(string) string { return "" }))
}
