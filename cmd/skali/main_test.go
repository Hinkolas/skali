package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/installer"
	versionpkg "github.com/Hinkolas/skali/internal/version"
)

// TestCommandVerbConsistency keeps the noun-verb surface uniform: the
// canonical spellings are list and remove, every one of them also answers
// to ls and rm, no command is named by the short form, and argument
// placeholders use one notation. Refs #18.
func TestCommandVerbConsistency(t *testing.T) {
	var walk func(command *cobra.Command)
	walk = func(command *cobra.Command) {
		path := command.CommandPath()
		switch command.Name() {
		case "list":
			require.Contains(t, command.Aliases, "ls", "%s lacks the ls alias", path)
		case "remove":
			require.Contains(t, command.Aliases, "rm", "%s lacks the rm alias", path)
		case "ls", "rm", "delete", "del":
			t.Errorf("%s: use list or remove as the canonical verb", path)
		}
		if short := command.Short; short != "" {
			require.Equal(t, strings.ToUpper(short[:1]), short[:1], "%s: Short must start uppercase", path)
			require.False(t, strings.HasSuffix(short, "."), "%s: Short must not end with a period", path)
		}
		if long := command.Long; long != "" {
			require.True(t, strings.HasSuffix(strings.TrimSpace(long), ".") || strings.HasSuffix(strings.TrimSpace(long), ")"), "%s: Long must end with a sentence", path)
		}
		for _, token := range strings.Fields(command.Use)[1:] {
			if strings.HasPrefix(token, "-") {
				continue
			}
			bare := strings.TrimSuffix(token, "...")
			if strings.HasPrefix(bare, "<") && strings.HasSuffix(bare, ">") {
				continue
			}
			if bare == "--" {
				continue
			}
			if token == "[flags]" || (strings.HasPrefix(bare, "[") && strings.HasSuffix(bare, "]")) {
				continue
			}
			t.Errorf("%s: placeholder %q must be <name>, [name], or --", path, token)
		}
		for _, child := range command.Commands() {
			walk(child)
		}
	}
	walk(newRootCommand())
}

func TestRemoteVersionLines(t *testing.T) {
	cacheDir := t.TempDir()
	cached := installer.CLICachePath(cacheDir, "v0.4.0")
	require.NoError(t, installer.StoreBinary(cached, []byte("x"), "00"))
	cfg := &cliconfig.Config{Remotes: map[string]*cliconfig.Remote{
		"khz":   {Master: "https://khz.example/api", Version: "v0.4.0"},
		"lab":   {Master: "https://lab.example/api", Version: "v0.3.2"},
		"home":  {Master: "https://home.example/api", Version: "v0.5.0"},
		"tree":  {Master: "https://tree.example/api", Version: "v0.0.0-dev"},
		"fresh": {Master: "https://fresh.example/api"},
		"local": {Master: "http://skali.localhost:7070", Version: "v0.4.0"},
	}}
	require.Equal(t, []string{
		"remote home  skalid v0.5.0 (this binary)",
		"remote khz  skalid v0.4.0 (cached)",
		"remote lab  skalid v0.3.2 (not cached)",
		"remote tree  skalid v0.0.0-dev (development build, not dispatched)",
	}, remoteVersionLines(cfg, "v0.5.0", cacheDir))
	require.Empty(t, remoteVersionLines(&cliconfig.Config{}, "v0.5.0", cacheDir))
}

func TestVersionCommandFirstLineIsStable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	seedConfig(t, &cliconfig.Config{CurrentRemote: "khz", Remotes: map[string]*cliconfig.Remote{
		"khz": {Master: "https://khz.example/api", Version: "v0.4.0"},
	}})
	out, err := runCapturingStdout(t, func() error { return execute(newRootCommand(), "version") })
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Equal(t, "skali version "+versionpkg.Version, lines[0])
	require.Len(t, lines, 2)
	require.Contains(t, lines[1], "remote khz  skalid v0.4.0")
}
