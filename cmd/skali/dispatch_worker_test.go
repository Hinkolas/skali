package main

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/stretchr/testify/require"
)

// Exercise the real process/environment boundary. While the launcher is about
// to spawn, another invocation changes both current remote and checkout binding.
// References, validation and deployment must still use the frozen selection.
func TestDispatchWorkerUsesFrozenContext(t *testing.T) {
	f := newDispatchFixture(t, "v0.5.0")
	binary := filepath.Join(t.TempDir(), "skali")
	build := exec.Command("go", "build", "-ldflags", "-X github.com/Hinkolas/skali/internal/version.Version=v0.4.0", "-o", binary, "./cmd/skali")
	build.Dir = "../.."
	output, err := build.CombinedOutput()
	require.NoError(t, err, "%s", output)
	data, err := os.ReadFile(binary)
	require.NoError(t, err)
	path := installer.CLICachePath(f.cache, "v0.4.0")
	require.NoError(t, installer.StoreBinary(path, data, checksumEntry("skali", data)[:64]))
	var chosenRequests, otherRequests atomic.Int32
	selected := fakeMaster(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(client.VersionHeader, "v0.4.0")
		w.Header().Set(client.InstanceHeader, "inst-1")
		if r.URL.Path == "/healthz" {
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		chosenRequests.Add(1)
		w.WriteHeader(422)
		_, _ = w.Write([]byte(`{"error":{"code":"test_stop","message":"frozen target reached"}}`))
	}))
	other := fakeMaster(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { otherRequests.Add(1); w.WriteHeader(500) }))
	require.NoError(t, os.WriteFile(filepath.Join(f.d.cwd, "skali.yml"), []byte("skali: v0.4.0\n"+upgradeFixtureBody), 0600))
	f.d.environ = os.Environ
	var workerOutput string
	f.d.spawn = func(ctx context.Context, path string, args, env []string) (childStatus, error) {
		require.NoError(t, cliconfig.Update(func(c *cliconfig.Config) error { c.CurrentRemote = "other"; return nil }))
		require.NoError(t, checkoutSaveForTest(f.d.cwd, other.URL))
		command := exec.CommandContext(ctx, path, args...)
		command.Dir = f.d.cwd
		command.Env = env
		out, err := command.CombinedOutput()
		workerOutput = string(out)
		if exit, ok := err.(*exec.ExitError); ok {
			return childStatus{Code: exit.ExitCode()}, nil
		}
		return childStatus{}, err
	}
	for _, args := range [][]string{{"skill", "read", "manifest"}, {"validate"}, {"deploy", "--yes", "--environment", "production"}} {
		seedConfig(t, &cliconfig.Config{CurrentRemote: "selected", Remotes: map[string]*cliconfig.Remote{
			"selected": {Master: selected.URL, Token: "tok", Instance: "inst-1", Version: "v0.5.0"}, "other": {Master: other.URL},
		}})
		require.NoError(t, checkoutSaveForTest(f.d.cwd, selected.URL))
		f.d.args = args
		handled, code := f.d.run()
		require.True(t, handled)
		if args[0] == "deploy" {
			require.Equal(t, 1, code)
			require.Contains(t, workerOutput, "frozen target reached")
		} else {
			require.Zero(t, code, "%s", workerOutput)
		}
		if args[0] == "skill" {
			require.Contains(t, workerOutput, "skali v0.4.0; target: selected; source: checkout binding; mode: verified")
		}
		require.Equal(t, "v0.4.0", f.d.selected.Release)
		require.Equal(t, "selected", f.d.selected.Remote)
	}
	require.Positive(t, chosenRequests.Load())
	require.Zero(t, otherRequests.Load())
}
