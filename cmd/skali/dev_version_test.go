package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/cliconfig"
)

func TestDevVersionReason(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := &cliconfig.Config{CurrentRemote: "khz", Remotes: map[string]*cliconfig.Remote{
		"khz": {Master: "https://khz.example/api", Version: "v0.4.0"},
		"lab": {Master: "https://lab.example/api", Version: "v0.3.2"},
		"new": {Master: "https://new.example/api"},
	}}
	outside := t.TempDir()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "skali.yml"), []byte("project:\n  name: demo\n"), 0o644))
	require.NoError(t, checkoutSaveForTest(root, "https://lab.example/api"))

	withCLIVersion(t, "v0.0.0-dev")
	require.Equal(t, "local platform runs the working tree (cluster skali-dev-working-tree)",
		devVersionReason(cfg, "", outside, ""))

	withCLIVersion(t, "v0.4.0")
	head := "local platform runs skalid v0.4.0 (cluster skali-dev-v0-4-0): "
	require.Equal(t, head+"the release khz runs, the current remote", devVersionReason(cfg, "", outside, ""))
	require.Equal(t, head+"this skali's release; lab runs skalid v0.3.2 and dispatch did not run, see --verbose",
		devVersionReason(cfg, "", root, ""), "the binding wins over the current remote")
	require.Equal(t, head+"this skali's release; lab runs skalid v0.3.2 and dispatch did not run, see --verbose",
		devVersionReason(cfg, "", outside, "lab"))
	require.Equal(t, head+"this skali's release (new has not named a release yet)", devVersionReason(cfg, "", outside, "new"))
	require.Equal(t, head+"this skali's release", devVersionReason(cfg, "", outside, "nope"))

	withCLIVersion(t, "v0.3.2")
	require.Equal(t, "local platform runs skalid v0.3.2 (cluster skali-dev-v0-3-2): the release lab runs, from .skali/target.yaml",
		devVersionReason(cfg, "", root, ""))
	require.Equal(t, "local platform runs skalid v0.3.2 (cluster skali-dev-v0-3-2): the release lab runs, --remote",
		devVersionReason(cfg, "", outside, "lab"))

	require.NoError(t, checkoutSaveForTest(root, "https://elsewhere.example/api"))
	require.Equal(t, "local platform runs skalid v0.3.2 (cluster skali-dev-v0-3-2): this skali's release; "+
		".skali/target.yaml names https://elsewhere.example/api but no remote here does",
		devVersionReason(cfg, "", root, ""))

	// No remote at all: pure local development at home.
	require.Equal(t, "local platform runs skalid v0.3.2 (cluster skali-dev-v0-3-2): this skali's release",
		devVersionReason(&cliconfig.Config{Remotes: map[string]*cliconfig.Remote{}}, "", outside, ""))
}
