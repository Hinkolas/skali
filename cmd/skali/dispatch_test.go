package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/installer"
)

func envMap(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestDispatchGate(t *testing.T) {
	t.Parallel()
	cacheDir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "skali")
	deploy := invocation{command: "deploy"}
	none := envMap(nil)

	require.Equal(t, "already dispatched", dispatchGate(deploy, envMap(map[string]string{envDispatched: "1"}), "v0.4.0", outside, cacheDir))
	require.Contains(t, dispatchGate(deploy, envMap(map[string]string{envNoDispatch: "1"}), "v0.4.0", outside, cacheDir), envNoDispatch)
	require.Contains(t, dispatchGate(deploy, none, "v0.0.0-dev", outside, cacheDir), "development build")
	require.Contains(t, dispatchGate(deploy, none, "v0.4.0-3-gabc1234", outside, cacheDir), "development build")
	require.Equal(t, "help", dispatchGate(invocation{command: "deploy", help: true}, none, "v0.4.0", outside, cacheDir))
	require.Equal(t, "help", dispatchGate(invocation{version: true}, none, "v0.4.0", outside, cacheDir))
	require.Equal(t, "no command", dispatchGate(invocation{}, none, "v0.4.0", outside, cacheDir))
	for name := range noDispatchCommands {
		require.Contains(t, dispatchGate(invocation{command: name}, none, "v0.4.0", outside, cacheDir), "always runs at home", name)
	}
	require.Empty(t, dispatchGate(deploy, none, "v0.4.0", outside, cacheDir))

	// A binary run straight from the cache never dispatches, also when the
	// cache directory is reached through a symlink.
	cached := installer.CLICachePath(cacheDir, "v0.3.2")
	require.NoError(t, os.MkdirAll(filepath.Dir(cached), 0o755))
	require.NoError(t, os.WriteFile(cached, []byte("x"), 0o755))
	require.Equal(t, "running from the cache", dispatchGate(deploy, none, "v0.4.0", cached, cacheDir))
	link := filepath.Join(t.TempDir(), "cache-link")
	require.NoError(t, os.Symlink(cacheDir, link))
	require.Equal(t, "running from the cache", dispatchGate(deploy, none, "v0.4.0", cached, link))
	require.Equal(t, "running from the cache", dispatchGate(deploy, none, "v0.4.0", installer.CLICachePath(link, "v0.3.2"), cacheDir))
}

func TestDispatchTarget(t *testing.T) {
	t.Parallel()
	for _, record := range []string{"", "v0.0.0-dev", "v0.4.0", "test", "v0.4.0-3-gabc1234"} {
		_, ok := dispatchTarget("v0.4.0", record)
		require.False(t, ok, "record %q", record)
	}
	want, ok := dispatchTarget("v0.4.0", "v0.3.2")
	require.True(t, ok)
	require.Equal(t, "v0.3.2", want)
	want, ok = dispatchTarget("v0.4.0", "v0.4.0-rc.1")
	require.True(t, ok)
	require.Equal(t, "v0.4.0-rc.1", want)
}

func TestExitCodeFor(t *testing.T) {
	t.Cleanup(skew.reset)
	withCLIVersion(t, "v0.3.2")
	mismatch := &client.APIError{Status: 409, Code: client.CodeCLIVersionMismatch, Message: "requires skali v0.4.0"}

	t.Setenv(envDispatched, "")
	require.Equal(t, 0, exitCodeFor(nil))
	require.Equal(t, 1, exitCodeFor(errors.New("boom")))
	require.Equal(t, 1, exitCodeFor(mismatch), "only a dispatched child signals")

	t.Setenv(envDispatched, "1")
	require.Equal(t, 0, exitCodeFor(nil))
	require.Equal(t, 1, exitCodeFor(errors.New("boom")))
	require.Equal(t, exitVersionMoved, exitCodeFor(mismatch))

	// An observed differing release counts too, whatever the error was.
	skew.record("khz", "v0.4.0")
	require.Equal(t, exitVersionMoved, exitCodeFor(errors.New("session expired")))
	skew.record("khz", "v0.3.2")
	require.Equal(t, 1, exitCodeFor(errors.New("session expired")))
}

// dispatchFixture is a dispatcher wired to temp config and cache
// directories, a captured stderr, and a scripted spawn.
type dispatchFixture struct {
	d       *dispatcher
	stderr  *bytes.Buffer
	cache   string
	spawns  []spawnCall
	script  []func(call spawnCall) (childStatus, error)
	cluster *fakeCluster
}

type spawnCall struct {
	path string
	args []string
	env  []string
}

func newDispatchFixture(t *testing.T, home string, args ...string) *dispatchFixture {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Cleanup(skew.reset)
	f := &dispatchFixture{stderr: &bytes.Buffer{}, cache: t.TempDir()}
	f.d = &dispatcher{
		args:        args,
		env:         envMap(nil),
		environ:     func() []string { return []string{"HOME=/nowhere", envDispatched + "=stale"} },
		homeVersion: home,
		installed:   home,
		executable:  filepath.Join(t.TempDir(), "skali"),
		cacheDir:    f.cache,
		cwd:         t.TempDir(),
		home:        t.TempDir(),
		goos:        runtime.GOOS,
		goarch:      runtime.GOARCH,
		releaseBase: deadURL(t),
		stderr:      f.stderr,
		root:        newRootCommand,
	}
	f.d.spawn = func(ctx context.Context, path string, args, env []string) (childStatus, error) {
		call := spawnCall{path: path, args: args, env: env}
		f.spawns = append(f.spawns, call)
		if len(f.script) == 0 {
			return childStatus{}, nil
		}
		next := f.script[0]
		f.script = f.script[1:]
		return next(call)
	}
	return f
}

// seedCache stores a fake binary for a release the way a fetch would.
func (f *dispatchFixture) seedCache(t *testing.T, release string) string {
	t.Helper()
	body := fakeCLI(release)
	sum := checksumEntry("skali", body)[:64]
	path := installer.CLICachePath(f.cache, release)
	require.NoError(t, installer.StoreBinary(path, body, sum))
	return path
}

func (f *dispatchFixture) seedRemote(t *testing.T, name, master, version string) {
	t.Helper()
	cfg := &cliconfig.Config{CurrentRemote: name, Remotes: map[string]*cliconfig.Remote{
		name: {Master: master, Token: "tok", Instance: "inst-1", Version: version},
	}}
	seedConfig(t, cfg)
}

func TestDispatchRunsCachedRelease(t *testing.T) {
	f := newDispatchFixture(t, "v0.5.0", "env", "list")
	f.seedRemote(t, "khz", deadURL(t), "v0.4.0")
	path := f.seedCache(t, "v0.4.0")

	handled, code := f.d.run()
	require.True(t, handled)
	require.Equal(t, 0, code)
	require.Len(t, f.spawns, 1)
	require.Equal(t, path, f.spawns[0].path)
	require.Equal(t, []string{"env", "list"}, f.spawns[0].args)
	require.Equal(t, []string{"HOME=/nowhere", envDispatched + "=1"}, f.spawns[0].env)
	require.Empty(t, f.stderr.String(), "a cache hit prints nothing")
}

func TestDispatchSkipsWhenRecordMatchesHome(t *testing.T) {
	f := newDispatchFixture(t, "v0.4.0", "--verbose", "env", "list")
	f.seedRemote(t, "khz", deadURL(t), "v0.4.0")

	handled, _ := f.d.run()
	require.False(t, handled)
	require.Empty(t, f.spawns)
	require.Contains(t, f.stderr.String(), "dispatch: remote khz runs skalid \"v0.4.0\", this skali is v0.4.0")
}

func TestDispatchProbesEmptyRecord(t *testing.T) {
	f := newDispatchFixture(t, "v0.5.0", "env", "list")
	cluster := newFakeCluster(t, "v0.4.0", fakeCLI("v0.4.0"))
	f.seedRemote(t, "khz", cluster.URL, "")

	handled, code := f.d.run()
	require.True(t, handled)
	require.Equal(t, 0, code)
	require.Equal(t, "v0.4.0", loadConfig(t).Remotes["khz"].Version, "the probe fills the record")
	require.Len(t, f.spawns, 1)
	require.Equal(t, installer.CLICachePath(f.cache, "v0.4.0"), f.spawns[0].path)
	require.Contains(t, f.stderr.String(), "fetching skali v0.4.0 for remote khz\n")
}

func TestDispatchUnreachableEmptyRecordRunsHome(t *testing.T) {
	f := newDispatchFixture(t, "v0.5.0", "env", "list")
	f.seedRemote(t, "khz", deadURL(t), "")

	handled, _ := f.d.run()
	require.False(t, handled)
	require.Empty(t, f.spawns)
	require.Empty(t, f.stderr.String())
}

func TestDispatchRedispatchOnce(t *testing.T) {
	f := newDispatchFixture(t, "v0.5.0", "env", "list")
	cluster := newFakeCluster(t, "v0.3.2", fakeCLI("v0.3.2"))
	f.seedRemote(t, "khz", cluster.URL, "v0.4.0")
	first := f.seedCache(t, "v0.4.0")
	// The first child finds the daemon moved: it records the new version
	// (as remoteClient does) and exits with the reserved status.
	f.script = []func(spawnCall) (childStatus, error){
		func(spawnCall) (childStatus, error) {
			cfg := loadConfig(t)
			cfg.Remotes["khz"].Version = "v0.3.2"
			seedConfig(t, cfg)
			return childStatus{Code: exitVersionMoved}, nil
		},
		func(spawnCall) (childStatus, error) { return childStatus{Code: 0}, nil },
	}

	handled, code := f.d.run()
	require.True(t, handled)
	require.Equal(t, 0, code)
	require.Len(t, f.spawns, 2)
	require.Equal(t, first, f.spawns[0].path)
	require.Equal(t, installer.CLICachePath(f.cache, "v0.3.2"), f.spawns[1].path)
	require.Contains(t, f.stderr.String(), "hint: remote khz moved to skalid v0.3.2; rerunning with skali v0.3.2")
}

func TestDispatchSecondMoveIsNotRerun(t *testing.T) {
	f := newDispatchFixture(t, "v0.5.0", "env", "list")
	cluster := newFakeCluster(t, "v0.3.2", fakeCLI("v0.3.2"))
	f.seedRemote(t, "khz", cluster.URL, "v0.4.0")
	f.seedCache(t, "v0.4.0")
	move := func(to string) func(spawnCall) (childStatus, error) {
		return func(spawnCall) (childStatus, error) {
			cfg := loadConfig(t)
			cfg.Remotes["khz"].Version = to
			seedConfig(t, cfg)
			return childStatus{Code: exitVersionMoved}, nil
		}
	}
	f.script = []func(spawnCall) (childStatus, error){move("v0.3.2"), move("v0.3.1")}

	handled, code := f.d.run()
	require.True(t, handled)
	require.Equal(t, exitVersionMoved, code, "the second 213 is returned as is")
	require.Len(t, f.spawns, 2)
}

func TestDispatchPassesThrough213WhenNothingMoved(t *testing.T) {
	// skali exec passes a remote process's exit status through; 213 from
	// it must not trigger a rerun.
	f := newDispatchFixture(t, "v0.5.0", "exec", "web", "--", "sh", "-c", "exit 213")
	f.seedRemote(t, "khz", deadURL(t), "v0.4.0")
	f.seedCache(t, "v0.4.0")
	f.script = []func(spawnCall) (childStatus, error){
		func(spawnCall) (childStatus, error) { return childStatus{Code: exitVersionMoved}, nil },
	}

	handled, code := f.d.run()
	require.True(t, handled)
	require.Equal(t, exitVersionMoved, code)
	require.Len(t, f.spawns, 1)
	require.Empty(t, f.stderr.String())
}

func TestDispatchMoveToHomeRunsInProcess(t *testing.T) {
	f := newDispatchFixture(t, "v0.5.0", "env", "list")
	f.seedRemote(t, "khz", deadURL(t), "v0.4.0")
	f.seedCache(t, "v0.4.0")
	f.script = []func(spawnCall) (childStatus, error){
		func(spawnCall) (childStatus, error) {
			cfg := loadConfig(t)
			cfg.Remotes["khz"].Version = "v0.5.0"
			seedConfig(t, cfg)
			return childStatus{Code: exitVersionMoved}, nil
		},
	}

	handled, _ := f.d.run()
	require.False(t, handled, "home is the right release now; the caller runs the command")
	require.Len(t, f.spawns, 1)
	require.Contains(t, f.stderr.String(), "hint: remote khz now runs skalid v0.5.0; rerunning with this skali")
}

func TestDispatchStartFailureRunsHome(t *testing.T) {
	f := newDispatchFixture(t, "v0.5.0", "env", "list")
	f.seedRemote(t, "khz", deadURL(t), "v0.4.0")
	path := f.seedCache(t, "v0.4.0")
	f.script = []func(spawnCall) (childStatus, error){
		func(spawnCall) (childStatus, error) { return childStatus{}, os.ErrNotExist },
	}

	handled, _ := f.d.run()
	require.False(t, handled)
	require.Contains(t, f.stderr.String(), "warning: cached skali v0.4.0 could not start")
	_, err := os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist, "the unusable entry is dropped")
}

func TestDispatchHonorsRemoteOverrideAndBinding(t *testing.T) {
	f := newDispatchFixture(t, "v0.5.0", "logs", "--remote", "lab")
	cfg := &cliconfig.Config{CurrentRemote: "khz", Remotes: map[string]*cliconfig.Remote{
		"khz": {Master: deadURL(t), Token: "tok", Version: "v0.4.0"},
		"lab": {Master: deadURL(t), Token: "tok", Version: "v0.3.2"},
	}}
	seedConfig(t, cfg)
	lab := f.seedCache(t, "v0.3.2")
	f.seedCache(t, "v0.4.0")

	handled, _ := f.d.run()
	require.True(t, handled)
	require.Equal(t, lab, f.spawns[0].path, "--remote picks the binary")

	// A checkout binding beats the current remote.
	f.d.args = []string{"logs"}
	f.spawns = nil
	require.NoError(t, os.WriteFile(filepath.Join(f.d.cwd, "skali.yml"), []byte("project:\n  name: demo\n"), 0o644))
	require.NoError(t, checkoutSaveForTest(f.d.cwd, cfg.Remotes["lab"].Master))
	handled, _ = f.d.run()
	require.True(t, handled)
	require.Equal(t, lab, f.spawns[0].path, "the binding picks the binary")
}

func TestDispatchPromotesHomeToNewerRelease(t *testing.T) {
	f := newDispatchFixture(t, "v0.3.2", "env", "list")
	dir := t.TempDir()
	f.d.executable = writeExecutable(t, dir, "skali", fakeCLI("v0.3.2"))
	cluster := newFakeCluster(t, "v0.4.0", fakeCLI("v0.4.0"))
	f.seedRemote(t, "khz", cluster.URL, "v0.4.0")

	handled, code := f.d.run()
	require.True(t, handled)
	require.Equal(t, 0, code)
	installed, err := os.ReadFile(f.d.executable)
	require.NoError(t, err)
	require.Equal(t, fakeCLI("v0.4.0"), installed, "home was replaced")
	require.Contains(t, f.stderr.String(), "upgraded skali v0.3.2 -> v0.4.0 (remote khz runs skalid v0.4.0)")
	require.Equal(t, installer.CLICachePath(f.cache, "v0.4.0"), f.spawns[0].path, "the cached copy runs the command")
}

func TestRerunAfterMismatch(t *testing.T) {
	t.Cleanup(skew.reset)
	withCLIVersion(t, "v0.4.0")
	mismatch := &client.APIError{Status: 409, Code: client.CodeCLIVersionMismatch, Message: "requires skali v0.5.0"}
	calls := 0
	run := func() (bool, int) { calls++; return true, 0 }
	var stderr bytes.Buffer
	none := envMap(nil)

	// Not a refusal, or no release remote observed: nothing happens.
	handled, _ := rerunAfterMismatch(errors.New("boom"), none, &stderr, false, run)
	require.False(t, handled)
	handled, _ = rerunAfterMismatch(mismatch, none, &stderr, false, run)
	require.False(t, handled, "no remote recorded")
	skew.record(localRemoteName, "v0.5.0")
	handled, _ = rerunAfterMismatch(mismatch, none, &stderr, false, run)
	require.False(t, handled, "the local platform is not dispatched")
	skew.record("khz", "v0.0.0-dev")
	handled, _ = rerunAfterMismatch(mismatch, none, &stderr, false, run)
	require.False(t, handled, "a development daemon is never a target")
	require.Zero(t, calls)
	require.Empty(t, stderr.String())

	// A released remote that moved: one hint, then the dispatcher runs.
	skew.record("khz", "v0.5.0")
	handled, code := rerunAfterMismatch(mismatch, none, &stderr, false, run)
	require.True(t, handled)
	require.Equal(t, 0, code)
	require.Equal(t, 1, calls)
	require.Equal(t, "hint: remote khz now runs skalid v0.5.0; rerunning with skali v0.5.0\n", stderr.String())

	// Never from a child or with dispatch off.
	for _, key := range []string{envDispatched, envNoDispatch} {
		handled, _ = rerunAfterMismatch(mismatch, envMap(map[string]string{key: "1"}), &stderr, false, run)
		require.False(t, handled, key)
	}
	require.Equal(t, 1, calls)

	// Nor after a dispatch attempt in this process: the fetch already
	// failed once and said so.
	handled, _ = rerunAfterMismatch(mismatch, none, &stderr, true, run)
	require.False(t, handled)
	require.Equal(t, 1, calls)
}

func TestDispatchPromotesTwiceAcrossARerun(t *testing.T) {
	// Home v0.3.2 meets a v0.4.0 record, is promoted, and the child then
	// finds the cluster already at v0.5.0: the second promotion names the
	// version on disk, not the one still running.
	f := newDispatchFixture(t, "v0.3.2", "env", "list")
	f.d.executable = writeExecutable(t, t.TempDir(), "skali", fakeCLI("v0.3.2"))
	cluster := newFakeCluster(t, "v0.4.0", fakeCLI("v0.4.0"))
	f.seedRemote(t, "khz", cluster.URL, "v0.4.0")
	f.script = []func(spawnCall) (childStatus, error){
		func(spawnCall) (childStatus, error) {
			cluster.set(func(c *fakeCluster) { c.version, c.binary = "v0.5.0", fakeCLI("v0.5.0") })
			cfg := loadConfig(t)
			cfg.Remotes["khz"].Version = "v0.5.0"
			seedConfig(t, cfg)
			return childStatus{Code: exitVersionMoved}, nil
		},
		func(spawnCall) (childStatus, error) { return childStatus{}, nil },
	}

	handled, code := f.d.run()
	require.True(t, handled)
	require.Equal(t, 0, code)
	require.Contains(t, f.stderr.String(), "upgraded skali v0.3.2 -> v0.4.0 (remote khz runs skalid v0.4.0)")
	require.Contains(t, f.stderr.String(), "upgraded skali v0.4.0 -> v0.5.0 (remote khz runs skalid v0.5.0)")
	installed, err := os.ReadFile(f.d.executable)
	require.NoError(t, err)
	require.Equal(t, fakeCLI("v0.5.0"), installed)
	require.Equal(t, installer.CLICachePath(f.cache, "v0.5.0"), f.spawns[1].path)
}
