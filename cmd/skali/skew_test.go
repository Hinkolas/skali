package main

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/cliconfig"
)

func TestSkewHintRemote(t *testing.T) {
	require.Equal(t,
		"hint: remote khz runs skalid v0.4.0 and this CLI is v0.3.2; run skali upgrade --version v0.4.0 to match it, or download it from https://github.com/Hinkolas/skali/releases/tag/v0.4.0",
		skewHint("khz", "v0.3.2", "v0.4.0"))
	// The fix is the same command in both directions: upgrade pins any
	// exact release, downgrades included.
	require.Equal(t,
		"hint: remote khz runs skalid v0.3.0 and this CLI is v0.4.0; run skali upgrade --version v0.3.0 to match it, or download it from https://github.com/Hinkolas/skali/releases/tag/v0.3.0",
		skewHint("khz", "v0.4.0", "v0.3.0"))
	// A prerelease of the same number is another release.
	require.Contains(t, skewHint("khz", "v0.4.0-rc.1", "v0.4.0"), "--version v0.4.0 ")
	// A master that matches no stored remote still gets a sentence.
	require.Contains(t, skewHint("", "v0.3.2", "v0.4.0"), "hint: the remote runs skalid v0.4.0")
}

// Changing the installed local platform release requires explicit reset.
func TestSkewHintLocalRemote(t *testing.T) {
	for _, c := range []struct{ cli, server string }{{"v0.4.0", "v0.3.0"}, {"v0.3.2", "v0.4.0"}} {
		hint := skewHint(localRemoteName, c.cli, c.server)
		require.Contains(t, hint, "the local platform answering runs skalid "+c.server+" and this CLI is "+c.cli)
		require.Contains(t, hint, "run skali dev reset")
		require.NotContains(t, hint, "--version")
	}
}

// Nothing is said unless both sides are releases that differ: the daemon
// applies the same rule, so a silent CLI is never a gated one.
func TestSkewHintQuiet(t *testing.T) {
	for _, c := range []struct{ cli, server string }{
		{"v0.4.0", "v0.4.0"},
		{"v0.0.0-dev", "v0.4.0"},
		{"v0.1.0-3-gabc1234", "v0.4.0"},
		{"v0.4.0", "v0.0.0-dev"},
		{"v0.4.0", "test"},
		{"v0.4.0", ""},
	} {
		require.Empty(t, skewHint("khz", c.cli, c.server), "cli %q server %q", c.cli, c.server)
		require.Empty(t, skewHint(localRemoteName, c.cli, c.server), "local, cli %q server %q", c.cli, c.server)
	}
}

// devSkewError refuses a released local platform of another version ahead
// of the daemon's own refusal, and passes everything without a comparable
// version.
func TestDevSkewError(t *testing.T) {
	withCLIVersion(t, "v0.2.0")

	err := devSkewError("ghcr.io/hinkolas/skalid:v0.1.0")
	require.Error(t, err)
	require.Contains(t, err.Error(), "the local platform skali-dev runs skalid v0.1.0 and this CLI is v0.2.0")
	require.Contains(t, err.Error(), "skali dev reset")
	require.NotContains(t, err.Error(), "hint:")

	err = devSkewError("ghcr.io/hinkolas/skalid:v0.3.0")
	require.Error(t, err)
	require.Contains(t, err.Error(), "skali dev reset")

	// A prerelease of the CLI's version is another release.
	require.ErrorContains(t, devSkewError("ghcr.io/hinkolas/skalid:v0.2.0-rc.1"), "skali dev reset")

	require.NoError(t, devSkewError("ghcr.io/hinkolas/skalid:v0.2.0"))
	// Non-published platforms have no comparable version.
	require.NoError(t, devSkewError("skalid:dev"))
	require.NoError(t, devSkewError("registry.example.com/skalid:v0.1.0"))
}

func TestDevSkewErrorDevBuildCLI(t *testing.T) {
	withCLIVersion(t, "v0.0.0-dev")
	require.NoError(t, devSkewError("ghcr.io/hinkolas/skalid:v0.1.0"))
	require.NoError(t, devSkewError("ghcr.io/hinkolas/skalid:v9.9.9"))
}

// A client built for a stored remote feeds the recorder with the remote's
// name, and the pending hint names it.
func TestRemoteClientRecordsSkew(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Cleanup(skew.reset)
	withCLIVersion(t, "v1.0.0")
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Skali-Version", "v9.9.9")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	srv := fakeMaster(t, mux)
	seedConfig(t, &cliconfig.Config{
		CurrentRemote: "myremote",
		Remotes:       map[string]*cliconfig.Remote{"myremote": {Master: srv.URL, Token: "tok"}},
	})

	require.Empty(t, pendingSkewHint(), "nothing observed yet")
	_, _, api, err := currentClient()
	require.NoError(t, err)
	require.NoError(t, api.Health(context.Background()))

	remote, server := skew.snapshot()
	require.Equal(t, "myremote", remote)
	require.Equal(t, "v9.9.9", server)
	require.Equal(t,
		"hint: remote myremote runs skalid v9.9.9 and this CLI is v1.0.0; run skali upgrade --version v9.9.9 to match it, or download it from https://github.com/Hinkolas/skali/releases/tag/v9.9.9",
		pendingSkewHint())

	// A development CLI observes the same version and says nothing.
	withCLIVersion(t, "v0.0.0-dev")
	require.Empty(t, pendingSkewHint())
}
