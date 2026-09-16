package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/checkout"
	"github.com/Hinkolas/skali/internal/cliconfig"
)

// checkoutSaveForTest binds root to master the way a first deploy does.
func checkoutSaveForTest(root, master string) error {
	return checkout.Save(root, &checkout.Target{Master: master, Project: "demo", Environment: "production"})
}

func TestResolveRemoteTarget(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := &cliconfig.Config{CurrentRemote: "khz", Remotes: map[string]*cliconfig.Remote{
		"khz": {Master: "https://khz.example/api", Version: "v0.4.0"},
		"lab": {Master: "https://lab.example/api", Version: "v0.3.2"},
	}}
	outside := t.TempDir()

	target, err := resolveRemoteTarget(cfg, "", outside, "lab")
	require.NoError(t, err)
	require.Equal(t, "lab", target.Name)
	require.Nil(t, target.Binding, "an override ignores the checkout")

	_, err = resolveRemoteTarget(cfg, "", outside, "nope")
	require.ErrorContains(t, err, `remote "nope" does not exist`)

	target, err = resolveRemoteTarget(cfg, "", outside, "")
	require.NoError(t, err)
	require.Equal(t, "khz", target.Name, "the current remote outside a checkout")

	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "skali.yml"), []byte("project:\n  name: demo\n"), 0o644))
	require.NoError(t, checkoutSaveForTest(root, "https://lab.example/api/"))
	nested := filepath.Join(root, "services", "web")
	require.NoError(t, os.MkdirAll(nested, 0o755))
	target, err = resolveRemoteTarget(cfg, "", nested, "")
	require.NoError(t, err)
	require.Equal(t, "lab", target.Name, "the binding wins over the current remote")
	require.NotNil(t, target.Binding)
	require.Equal(t, "demo", target.Binding.Project)

	// An explicit manifest names the checkout wherever the cwd is.
	target, err = resolveRemoteTarget(cfg, filepath.Join(root, "skali.yml"), outside, "")
	require.NoError(t, err)
	require.Equal(t, "lab", target.Name)

	require.NoError(t, checkoutSaveForTest(root, "https://elsewhere.example/api"))
	_, err = resolveRemoteTarget(cfg, "", root, "")
	require.ErrorContains(t, err, "no remote for https://elsewhere.example/api on this machine")

	cfg.CurrentRemote = ""
	_, err = resolveRemoteTarget(cfg, "", outside, "")
	require.ErrorContains(t, err, "no remote selected")
}

func TestRemoteClientRecordsVersion(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Cleanup(skew.reset)
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Skali-Version", "v0.4.0")
		w.Header().Set("Skali-Instance", "inst-1")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	srv := fakeMaster(t, mux)
	seedConfig(t, &cliconfig.Config{CurrentRemote: "khz", Remotes: map[string]*cliconfig.Remote{
		"khz": {Master: srv.URL, Token: "tok"},
	}})

	_, _, c, err := currentClient()
	require.NoError(t, err)
	require.NoError(t, c.Health(context.Background()))
	stored := loadConfig(t).Remotes["khz"]
	require.Equal(t, "v0.4.0", stored.Version, "the observed daemon version is recorded for dispatch")
	require.Equal(t, "inst-1", stored.Instance)
	remote, server := skew.snapshot()
	require.Equal(t, "khz", remote)
	require.Equal(t, "v0.4.0", server)
}
