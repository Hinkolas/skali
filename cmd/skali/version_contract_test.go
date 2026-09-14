package main

import (
	"context"

	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/filelock"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/manifest"
	"github.com/stretchr/testify/require"
)

func TestConcurrentPromotionsChooseNewestRelease(t *testing.T) {
	f := newDispatchFixture(t, "v0.4.0")
	f.d.executable = writeExecutable(t, t.TempDir(), "skali", fakeCLI("v0.4.0"))
	var wg sync.WaitGroup
	for _, release := range []string{"v0.5.0", "v0.6.0-rc.2", "v0.6.0-rc.1", "v0.3.0"} {
		copy := *f.d
		copy.stderr = &bytes.Buffer{}
		wg.Add(1)
		go func() { defer wg.Done(); copy.promoteHome(context.Background(), fakeCLI(release), release, "target") }()
	}
	wg.Wait()
	installed, err := installedCLIVersion(context.Background(), f.d.executable)
	require.NoError(t, err)
	require.Equal(t, "v0.6.0-rc.2", installed)
}

func TestUnwritableHomeStillExecutesMatchingCache(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write protected directories")
	}
	f := newDispatchFixture(t, "v0.3.0", "validate")
	dir := t.TempDir()
	f.d.executable = writeExecutable(t, dir, "skali", fakeCLI("v0.3.0"))
	require.NoError(t, os.Chmod(dir, 0555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0755) })
	f.seedRemote(t, "target", fakeDaemon(t, "v0.4.0").URL, "v0.4.0")
	path := f.seedCache(t, "v0.4.0")
	handled, code := f.d.run()
	require.True(t, handled)
	require.Zero(t, code)
	require.Len(t, f.spawns, 1)
	require.Equal(t, path, f.spawns[0].path)
	require.Contains(t, f.stderr.String(), "not writable")
}

func TestDynamicCompletionNeverFetchesOrPromotes(t *testing.T) {
	f := newDispatchFixture(t, "v0.3.0", "__complete", "env", "list", "")
	f.seedRemote(t, "target", deadURL(t), "v0.4.0")
	handled, code := f.d.run()
	require.True(t, handled)
	require.Zero(t, code)
	require.Empty(t, f.spawns)
	path := f.seedCache(t, "v0.4.0")
	handled, code = f.d.run()
	require.True(t, handled)
	require.Zero(t, code)
	require.Len(t, f.spawns, 1)
	require.Equal(t, path, f.spawns[0].path)
	require.Empty(t, f.stderr.String())
	require.Equal(t, "v0.3.0", f.d.installed)
}

func TestWorkerMarkerDoesNotLeakToGrandchildren(t *testing.T) {
	previous := invocationContext
	t.Cleanup(func() { invocationContext = previous })
	t.Setenv(envDispatched, "1")
	invocationContext = nil
	require.Empty(t, dispatchEnvironment(envDispatched))
	invocationContext = &versionContext{Parent: os.Getppid()}
	require.Equal(t, "1", dispatchEnvironment(envDispatched))
	withCLIVersion(t, "v0.4.0")
	ctx := versionContext{Parent: os.Getppid(), Release: "v0.3.0"}
	raw, err := json.Marshal(ctx)
	require.NoError(t, err)
	t.Setenv(envVersionContext, string(raw))
	_, err = contextFromEnvironment()
	require.ErrorContains(t, err, "expected v0.3.0")
	ctx.Parent = 0
	raw, err = json.Marshal(ctx)
	require.NoError(t, err)
	t.Setenv(envVersionContext, string(raw))
	selected, err := contextFromEnvironment()
	require.NoError(t, err)
	require.Nil(t, selected)
}

func TestOfflineResolutionFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, record   string
		cache, corrupt bool
		want           int
	}{
		{"missing record", "", false, false, 1},
		{"missing binary", "v0.4.0", false, false, 1},
		{"corrupt cache", "v0.4.0", true, true, 1},
		{"available", "v0.4.0", true, false, 0},
		{"unsupported", "v0.1.0-rc.2", true, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDispatchFixture(t, "v0.5.0", "validate", "--offline")
			f.seedRemote(t, "target", deadURL(t), tc.record)
			if tc.cache {
				path := f.seedCache(t, tc.record)
				if tc.corrupt {
					require.NoError(t, os.WriteFile(path, []byte("broken"), 0755))
				}
			}
			handled, code := f.d.run()
			require.True(t, handled)
			require.Equal(t, tc.want, code)
			require.NotContains(t, f.stderr.String(), "fetching")
			if tc.want == 0 {
				require.Equal(t, "offline", f.d.selected.Mode)
				require.Len(t, f.spawns, 1)
			} else {
				require.Empty(t, f.spawns)
			}
		})
	}
}

func TestOnlineResolutionRefreshesEvenMatchingRecord(t *testing.T) {
	f := newDispatchFixture(t, "v0.5.0", "validate")
	f.seedRemote(t, "target", fakeDaemon(t, "v0.4.0").URL, "v0.5.0")
	f.seedCache(t, "v0.4.0")
	handled, code := f.d.run()
	require.True(t, handled)
	require.Zero(t, code)
	require.Equal(t, "v0.4.0", f.d.selected.Release)
	require.Equal(t, "verified", f.d.selected.Mode)
}

func TestInvalidTargetNeverFallsThrough(t *testing.T) {
	for _, mode := range []string{"explicit", "binding", "config"} {
		t.Run(mode, func(t *testing.T) {
			f := newDispatchFixture(t, "v0.5.0", "validate", "--offline")
			switch mode {
			case "explicit":
				f.d.args = append(f.d.args, "--remote", "absent")
			case "binding":
				require.NoError(t, os.WriteFile(filepath.Join(f.d.cwd, "skali.yml"), []byte("name: demo"), 0600))
				require.NoError(t, checkoutSaveForTest(f.d.cwd, "https://unknown"))
			case "config":
				path, err := cliconfig.Path()
				require.NoError(t, err)
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
				require.NoError(t, os.WriteFile(path, []byte("remotes: [bad"), 0600))
			}
			handled, code := f.d.run()
			require.True(t, handled)
			require.Equal(t, 1, code)
			require.Empty(t, f.spawns)
		})
	}
}

func TestFrozenTargetSurvivesCurrentRemoteChange(t *testing.T) {
	f := newDispatchFixture(t, "v0.4.0", "skill", "read", "manifest")
	f.seedRemote(t, "selected", fakeDaemon(t, "v0.4.0").URL, "v0.4.0")
	handled, code := f.d.run()
	require.False(t, handled)
	require.Zero(t, code)
	previous := invocationContext
	invocationContext = f.d.selected
	t.Cleanup(func() { invocationContext = previous })
	require.NoError(t, cliconfig.Update(func(c *cliconfig.Config) error {
		c.Remotes["changed"] = &cliconfig.Remote{Master: "https://changed"}
		c.CurrentRemote = "changed"
		return nil
	}))
	cfg := loadConfig(t)
	for _, command := range []string{"reference", "validation", "deployment"} {
		target, err := resolveRemoteTarget(cfg, "", f.d.cwd, "")
		require.NoError(t, err, command)
		require.Equal(t, "selected", target.Name)
	}
	_, name, api, err := currentClient()
	require.NoError(t, err)
	require.Equal(t, "selected", name)
	require.Equal(t, f.d.selected.Master, api.Master())
	cfg.Remotes["selected"].Master = "https://replaced"
	_, err = resolveRemoteTarget(cfg, "", f.d.cwd, "")
	require.ErrorContains(t, err, "changed during")
}

func TestNoMutationReplay(t *testing.T) {
	for _, args := range [][]string{{"deploy"}, {"backup", "create"}, {"cluster", "upgrade", "--version", "v0.6.0"}, {"manifest", "upgrade"}} {
		f := newDispatchFixture(t, "v0.5.0", args...)
		f.seedRemote(t, "target", fakeDaemon(t, "v0.4.0").URL, "v0.4.0")
		f.seedCache(t, "v0.4.0")
		f.script = []func(spawnCall) (childStatus, error){func(spawnCall) (childStatus, error) {
			cfg := loadConfig(t)
			cfg.Remotes["target"].Version = "v0.6.0"
			seedConfig(t, cfg)
			return childStatus{Code: exitVersionMoved}, nil
		}}
		handled, _ := f.d.run()
		require.True(t, handled)
		require.Len(t, f.spawns, 1)
	}
}

func TestPromotionRechecksDiskAndKeepsNewerPrerelease(t *testing.T) {
	f := newDispatchFixture(t, "v0.4.0")
	f.d.executable = writeExecutable(t, t.TempDir(), "skali", fakeCLI("v0.6.0-rc.1"))
	require.False(t, f.d.promoteHome(context.Background(), fakeCLI("v0.5.0"), "v0.5.0", "target"))
	data, err := os.ReadFile(f.d.executable)
	require.NoError(t, err)
	require.Equal(t, fakeCLI("v0.6.0-rc.1"), data)
	require.True(t, f.d.promoteHome(context.Background(), fakeCLI("v0.6.0-rc.2"), "v0.6.0-rc.2", "target"))
}

func TestCachePruningRespectsExecutionLease(t *testing.T) {
	f := newDispatchFixture(t, "v0.5.0")
	path := f.seedCache(t, "v0.4.0")
	old := time.Now().Add(-2 * pruneGrace)
	require.NoError(t, os.Chtimes(filepath.Dir(path), old, old))
	unlock, err := filelock.Shared(context.Background(), installer.CLILockPath(f.cache, "v0.4.0"))
	require.NoError(t, err)
	pruneCLICache(&cliconfig.Config{}, "v0.5.0", f.cache)
	require.FileExists(t, path)
	unlock()
	pruneCLICache(&cliconfig.Config{}, "v0.5.0", f.cache)
	require.NoFileExists(t, path)
}

func TestManifestSemanticReviewDoesNotWrite(t *testing.T) {
	withCLIVersion(t, "v0.4.0")
	previous := manifest.Ledger
	t.Cleanup(func() { manifest.Ledger = previous })
	manifest.Ledger = append(append([]manifest.Change{}, previous...), manifest.Change{Release: "v0.4.0", Kind: manifest.ChangeChanged, Path: "applications.*.deployment.rollout.strategy", WhenOmitted: true, Message: "default changed", Hint: "review strategy"})
	path := writeManifestFixture(t, "skali: v0.3.0 # preserve\n")
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	err = execute(newRootCommand(), "manifest", "upgrade", "--manifest", path)
	require.ErrorContains(t, err, "applications.web.deployment.rollout.strategy")
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestAllReferencesCarryRuntimeContext(t *testing.T) {
	previous := invocationContext
	t.Cleanup(func() { invocationContext = previous })
	for _, mode := range []string{"verified", "offline", "home"} {
		withCLIVersion(t, "v0.4.0")
		if mode == "home" {
			withCLIVersion(t, "v0.0.0-dev")
		}
		invocationContext = &versionContext{Remote: "target", Source: "--remote", Mode: mode}
		for _, topic := range []string{"manifest", "cli", "architecture", ""} {
			args := []string{"skill", "read"}
			if topic != "" {
				args = append(args, topic)
			}
			out, err := runCapturingStdout(t, func() error { return execute(newRootCommand(), args...) })
			require.NoError(t, err)
			require.Contains(t, out, versionDescription()+"\n\n")
		}
	}
}

func TestVersionObservationUpdatesTheSelectedAlias(t *testing.T) {
	f := newDispatchFixture(t, "v0.5.0")
	server := fakeDaemon(t, "v0.4.0")
	seedConfig(t, &cliconfig.Config{CurrentRemote: "other", Remotes: map[string]*cliconfig.Remote{
		"selected": {Master: server.URL, Token: "selected-token", Instance: "inst-1", Version: "v0.3.0"},
		"other":    {Master: server.URL, Token: "other-token", Instance: "inst-1", Version: "v0.3.0"},
	}})
	cfg := loadConfig(t)
	require.NoError(t, remoteClient(cfg, cfg.Remotes["selected"]).Health(context.Background()))
	cfg = loadConfig(t)
	require.Equal(t, "v0.4.0", cfg.Remotes["selected"].Version)
	require.Equal(t, "v0.3.0", cfg.Remotes["other"].Version)
	require.Empty(t, f.spawns)
}
