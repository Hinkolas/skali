package main

import (
	"errors"
	"net/http"
	"os"
	"sync/atomic"
	"testing"

	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/filelock"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/stretchr/testify/require"
)

func TestHelpUsesRecordedCLIWithoutNetworkOrPromotion(t *testing.T) {
	for _, args := range [][]string{{"deploy", "--help"}, {"help", "deploy"}, {"deploy", "--help", "--offline"}, {"help", "deploy", "--offline"}} {
		t.Run(args[0]+args[1]+args[len(args)-1], func(t *testing.T) {
			f := newDispatchFixture(t, "v0.3.0", args...)
			var requests atomic.Int32
			server := fakeMaster(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(500) }))
			f.seedRemote(t, "target", server.URL, "v0.4.0")
			path := f.seedCache(t, "v0.4.0")
			handled, code := f.d.run()
			require.True(t, handled)
			require.Zero(t, code)
			require.Zero(t, requests.Load())
			require.Len(t, f.spawns, 1)
			require.Equal(t, path, f.spawns[0].path)
			require.NotContains(t, f.spawns[0].args, "--offline")
			_, err := os.Stat(f.d.executable)
			require.ErrorIs(t, err, os.ErrNotExist, "home is untouched")
			require.Contains(t, f.stderr.String(), "recorded release, not verified online")
		})
	}
}

func TestHelpFallsBackToLabeledHomeHelp(t *testing.T) {
	for _, scenario := range []string{"missing", "corrupt", "startup", "busy", "invalid-config", "unknown-remote", "early-release"} {
		t.Run(scenario, func(t *testing.T) {
			f := newDispatchFixture(t, "v0.3.0", "deploy", "--help")
			f.seedRemote(t, "target", deadURL(t), "v0.4.0")
			switch scenario {
			case "busy":
				f.seedCache(t, "v0.4.0")
				unlock, err := filelock.Try(installer.CLILockPath(f.cache, "v0.4.0"))
				require.NoError(t, err)
				require.NotNil(t, unlock)
				defer unlock()
			case "corrupt":
				path := f.seedCache(t, "v0.4.0")
				require.NoError(t, os.WriteFile(path, []byte("broken"), 0755))
			case "startup":
				f.seedCache(t, "v0.4.0")
				f.script = []func(spawnCall) (childStatus, error){func(spawnCall) (childStatus, error) { return childStatus{}, errors.New("cannot execute") }}
			case "invalid-config":
				path, err := cliconfig.Path()
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(path, []byte("remotes: ["), 0600))
			case "unknown-remote":
				f.d.args = append(f.d.args, "--remote", "unknown")
			case "early-release":
				f.seedRemote(t, "target", deadURL(t), "v0.1.0-rc.2")
			}
			handled, code := f.d.run()
			require.False(t, handled)
			require.Zero(t, code)
			require.Contains(t, f.stderr.String(), "help from skali v0.3.0 (home;")
			require.Contains(t, f.stderr.String(), "target compatibility is not verified")
			require.Equal(t, "home", f.d.selected.Mode)
			require.NoFileExists(t, f.d.executable)
		})
	}
}

func TestHelpOfflineModifierDoesNotEnableOfflineDeployment(t *testing.T) {
	args := helpArgs([]string{"deploy", "--offline"})
	require.Equal(t, []string{"deploy", "--offline"}, args)
	require.ErrorContains(t, execute(newRootCommand(), args...), "unknown flag: --offline")
	args = helpArgs([]string{"deploy", "--help", "--offline"})
	require.NoError(t, execute(newRootCommand(), args...))
	args = helpArgs([]string{"help", "deploy", "--offline"})
	require.NoError(t, execute(newRootCommand(), args...))
}

func TestOnlineDeployFailureDoesNotRecommendUnsupportedFlag(t *testing.T) {
	f := newDispatchFixture(t, "v0.3.0", "deploy")
	f.seedRemote(t, "target", deadURL(t), "v0.3.0")
	handled, code := f.d.run()
	require.True(t, handled)
	require.Equal(t, 1, code)
	require.NotContains(t, f.stderr.String(), "--offline")
	require.Contains(t, f.stderr.String(), "cluster availability")
}
