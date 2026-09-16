package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	require.Empty(t, dispatchGate(invocation{command: "deploy", help: true}, none, "v0.4.0", outside, cacheDir))
	require.Equal(t, "help", dispatchGate(invocation{version: true}, none, "v0.4.0", outside, cacheDir))
	require.Equal(t, "no command", dispatchGate(invocation{}, none, "v0.4.0", outside, cacheDir))
	for name := range noDispatchCommands {
		require.Contains(t, dispatchGate(invocation{command: name}, none, "v0.4.0", outside, cacheDir), "always runs at home", name)
	}
	require.Empty(t, dispatchGate(deploy, none, "v0.4.0", outside, cacheDir))
	// Dev creation follows the target; local stop/reset/status stay home.
	require.Empty(t, dispatchGate(invocation{command: "dev", path: "dev start"}, none, "v0.4.0", outside, cacheDir))
	require.Equal(t, "dev always runs at home", dispatchGate(invocation{command: "dev", path: "dev stop"}, none, "v0.4.0", outside, cacheDir))
	// skill read serves the reference of the cluster that will compile the
	// manifest; install writes the neutral shell and stays with the newest
	// binary.
	require.Empty(t, dispatchGate(invocation{command: "skill", path: "skill read"}, none, "v0.4.0", outside, cacheDir))
	require.Equal(t, "skill always runs at home", dispatchGate(invocation{command: "skill", path: "skill install"}, none, "v0.4.0", outside, cacheDir))

	// Direct cache invocations still resolve the target; only a validated
	// worker context suppresses dispatch. Symlinks do not change the policy.
	cached := installer.CLICachePath(cacheDir, "v0.3.2")
	require.NoError(t, os.MkdirAll(filepath.Dir(cached), 0o755))
	require.NoError(t, os.WriteFile(cached, []byte("x"), 0o755))
	require.Empty(t, dispatchGate(deploy, none, "v0.4.0", cached, cacheDir))
	link := filepath.Join(t.TempDir(), "cache-link")
	require.NoError(t, os.Symlink(cacheDir, link))
	require.Empty(t, dispatchGate(deploy, none, "v0.4.0", cached, link))
	require.Empty(t, dispatchGate(deploy, none, "v0.4.0", installer.CLICachePath(link, "v0.3.2"), cacheDir))
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
	previous := invocationContext
	invocationContext = &versionContext{Parent: os.Getppid()}
	t.Cleanup(func() { invocationContext = previous })
	oldArgs := os.Args
	os.Args = []string{"skali", "env", "list"}
	t.Cleanup(func() { os.Args = oldArgs })
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
	require.Equal(t, 1, exitCodeFor(errors.New("session expired")))
	skew.record("khz", "v0.3.2")
	require.Equal(t, 1, exitCodeFor(errors.New("session expired")))
}

// dispatchFixture is a dispatcher wired to temp config and cache
// directories, a captured stderr, and a scripted spawn.
type dispatchFixture struct {
	d      *dispatcher
	stderr *bytes.Buffer
	cache  string
	spawns []spawnCall
	script []func(call spawnCall) (childStatus, error)
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
		executable:  filepath.Join(t.TempDir(), "skali"),
		cacheDir:    f.cache,
		cwd:         t.TempDir(),
		home:        t.TempDir(),
		goos:        runtime.GOOS,
		goarch:      runtime.GOARCH,
		releaseBase: deadURL(t),
		stderr:      f.stderr,
		root:        newRootCommand,
		// Not a terminal unless a test says so; a prompt nobody scripted
		// is a failure, not a hang.
		interactive: func() bool { return false },
		confirm: func(context.Context, string, string) (bool, error) {
			t.Fatal("unexpected upgrade prompt")
			return false, nil
		},
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

// serveFeed points the dispatcher at a release feed publishing one
// release: the fake CLI of that version with its checksum.
func (f *dispatchFixture) serveFeed(t *testing.T, version string) *fakeUpgradeServer {
	t.Helper()
	feed := newFakeUpgradeServer(t, version, releaseAssets(fakeCLI(version)))
	f.d.releaseBase, f.d.feedClient = feed.URL, feed.Client()
	return feed
}

// fakeDaemon plays a skalid of one version for the dispatcher's health
// probe: every response carries the version and identity headers.
func fakeDaemon(t *testing.T, version string) *httptest.Server {
	t.Helper()
	return fakeMaster(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(client.VersionHeader, version)
		w.Header().Set(client.InstanceHeader, "inst-1")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
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
	f.seedRemote(t, "khz", fakeDaemon(t, "v0.4.0").URL, "v0.4.0")
	path := f.seedCache(t, "v0.4.0")

	handled, code := f.d.run()
	require.True(t, handled)
	require.Equal(t, 0, code)
	require.Len(t, f.spawns, 1)
	require.Equal(t, path, f.spawns[0].path)
	require.Equal(t, []string{"env", "list"}, f.spawns[0].args)
	require.Contains(t, f.spawns[0].env, envDispatched+"=1")
	require.Contains(t, fmt.Sprint(f.spawns[0].env), `"release":"v0.4.0"`)
	require.Contains(t, f.stderr.String(), "verified")
}

func TestDispatchSkipsWhenRecordMatchesHome(t *testing.T) {
	f := newDispatchFixture(t, "v0.4.0", "--verbose", "env", "list")
	f.seedRemote(t, "khz", fakeDaemon(t, "v0.4.0").URL, "v0.4.0")

	handled, _ := f.d.run()
	require.False(t, handled)
	require.Empty(t, f.spawns)
	require.Contains(t, f.stderr.String(), "target khz: skali v0.4.0")
}

func TestDispatchProbesEmptyRecord(t *testing.T) {
	f := newDispatchFixture(t, "v0.5.0", "env", "list")
	f.serveFeed(t, "v0.4.0")
	f.seedRemote(t, "khz", fakeDaemon(t, "v0.4.0").URL, "")

	handled, code := f.d.run()
	require.True(t, handled)
	require.Equal(t, 0, code)
	require.Equal(t, "v0.4.0", loadConfig(t).Remotes["khz"].Version, "the probe fills the record")
	require.Len(t, f.spawns, 1)
	require.Equal(t, installer.CLICachePath(f.cache, "v0.4.0"), f.spawns[0].path)
	require.Contains(t, f.stderr.String(), "fetching skali v0.4.0 for remote khz\n")
}

func TestDispatchUnreachableEmptyRecordFailsClosed(t *testing.T) {
	f := newDispatchFixture(t, "v0.5.0", "env", "list")
	f.seedRemote(t, "khz", deadURL(t), "")

	handled, _ := f.d.run()
	require.True(t, handled)
	require.Empty(t, f.spawns)
	require.Contains(t, f.stderr.String(), "verify remote")
}

func TestDispatchRedispatchOnce(t *testing.T) {
	f := newDispatchFixture(t, "v0.5.0", "env", "list")
	f.serveFeed(t, "v0.3.2")
	f.seedRemote(t, "khz", fakeDaemon(t, "v0.4.0").URL, "v0.4.0")
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
	require.Contains(t, f.stderr.String(), "fetching skali v0.3.2")
}

func TestDispatchSecondMoveIsNotRerun(t *testing.T) {
	f := newDispatchFixture(t, "v0.5.0", "env", "list")
	f.serveFeed(t, "v0.3.2")
	f.seedRemote(t, "khz", fakeDaemon(t, "v0.4.0").URL, "v0.4.0")
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
	require.Equal(t, 1, code, "the second release change is an actionable error")
	require.Len(t, f.spawns, 2)
}

func TestDispatchPassesThrough213WhenNothingMoved(t *testing.T) {
	// skali exec passes a remote process's exit status through; 213 from
	// it must not trigger a rerun.
	f := newDispatchFixture(t, "v0.5.0", "exec", "web", "--", "sh", "-c", "exit 213")
	f.seedRemote(t, "khz", fakeDaemon(t, "v0.4.0").URL, "v0.4.0")
	f.seedCache(t, "v0.4.0")
	f.script = []func(spawnCall) (childStatus, error){
		func(spawnCall) (childStatus, error) { return childStatus{Code: exitVersionMoved}, nil },
	}

	handled, code := f.d.run()
	require.True(t, handled)
	require.Equal(t, exitVersionMoved, code)
	require.Len(t, f.spawns, 1)
	require.Contains(t, f.stderr.String(), "verified")
}

func TestDispatchMoveToHomePreservesFrozenContext(t *testing.T) {
	f := newDispatchFixture(t, "v0.5.0", "env", "list")
	f.seedRemote(t, "khz", fakeDaemon(t, "v0.4.0").URL, "v0.4.0")
	f.seedCache(t, "v0.4.0")
	f.seedCache(t, "v0.5.0")
	f.script = []func(spawnCall) (childStatus, error){
		func(spawnCall) (childStatus, error) {
			cfg := loadConfig(t)
			cfg.Remotes["khz"].Version = "v0.5.0"
			seedConfig(t, cfg)
			return childStatus{Code: exitVersionMoved}, nil
		},
	}

	handled, _ := f.d.run()
	require.True(t, handled)
	require.Len(t, f.spawns, 2)
	require.Equal(t, installer.CLICachePath(f.cache, "v0.5.0"), f.spawns[1].path)
}

func TestDispatchStartFailureFailsClosed(t *testing.T) {
	f := newDispatchFixture(t, "v0.5.0", "env", "list")
	f.seedRemote(t, "khz", fakeDaemon(t, "v0.4.0").URL, "v0.4.0")
	path := f.seedCache(t, "v0.4.0")
	f.script = []func(spawnCall) (childStatus, error){
		func(spawnCall) (childStatus, error) { return childStatus{}, os.ErrNotExist },
	}

	handled, _ := f.d.run()
	require.True(t, handled)
	require.Contains(t, f.stderr.String(), "error: cached skali v0.4.0 could not start")
	_, err := os.Stat(path)
	require.NoError(t, err, "startup failure does not mutate an acquired cache entry")
}

func TestDispatchHonorsRemoteOverrideAndBinding(t *testing.T) {
	f := newDispatchFixture(t, "v0.5.0", "logs", "--remote", "lab")
	cfg := &cliconfig.Config{CurrentRemote: "khz", Remotes: map[string]*cliconfig.Remote{
		"khz": {Master: fakeDaemon(t, "v0.4.0").URL, Token: "tok", Version: "v0.4.0"},
		"lab": {Master: fakeDaemon(t, "v0.3.2").URL, Token: "tok", Version: "v0.3.2"},
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

// consent makes the fixture a terminal whose user answers the upgrade
// prompt with yes (or no) and records what it asked.
func (f *dispatchFixture) consent(t *testing.T, yes bool) *[]string {
	t.Helper()
	asked := &[]string{}
	f.d.interactive = func() bool { return true }
	f.d.confirm = func(_ context.Context, title, description string) (bool, error) {
		*asked = append(*asked, title+" | "+description)
		return yes, nil
	}
	return asked
}

func TestDispatchRefusesNewerReleaseNonInteractive(t *testing.T) {
	f := newDispatchFixture(t, "v0.3.2", "env", "list")
	f.d.executable = writeExecutable(t, t.TempDir(), "skali", fakeCLI("v0.3.2"))
	feed := f.serveFeed(t, "v0.4.0")
	f.seedRemote(t, "khz", fakeDaemon(t, "v0.4.0").URL, "v0.4.0")

	handled, code := f.d.run()
	require.True(t, handled)
	require.Equal(t, 1, code)
	require.Empty(t, f.spawns, "nothing runs")
	require.Empty(t, feed.requested(), "nothing is downloaded before consent")
	require.Contains(t, f.stderr.String(), "error: remote khz runs skali v0.4.0, newer than this CLI v0.3.2; run skali upgrade --version v0.4.0 first")
	unchanged, err := os.ReadFile(f.d.executable)
	require.NoError(t, err)
	require.Equal(t, fakeCLI("v0.3.2"), unchanged)
	_, cached := installer.CachedBinary(installer.CLICachePath(f.cache, "v0.4.0"))
	require.False(t, cached)
}

func TestDispatchUpgradesHomeOnConsent(t *testing.T) {
	f := newDispatchFixture(t, "v0.3.2", "env", "list")
	f.d.executable = writeExecutable(t, t.TempDir(), "skali", fakeCLI("v0.3.2"))
	f.serveFeed(t, "v0.4.0")
	f.seedRemote(t, "khz", fakeDaemon(t, "v0.4.0").URL, "v0.4.0")
	asked := f.consent(t, true)

	handled, code := f.d.run()
	require.True(t, handled)
	require.Equal(t, 0, code)
	require.Equal(t, []string{"Upgrade skali v0.3.2 -> v0.4.0 now? | remote khz runs skali v0.4.0; a CLI at least as new is required to manage it"}, *asked)
	installed, err := os.ReadFile(f.d.executable)
	require.NoError(t, err)
	require.Equal(t, fakeCLI("v0.4.0"), installed, "home was replaced")
	require.Contains(t, f.stderr.String(), "upgraded skali v0.3.2 -> v0.4.0 (remote khz runs skalid v0.4.0)")
	require.Equal(t, installer.CLICachePath(f.cache, "v0.4.0"), f.spawns[0].path, "the cached copy runs the command")
}

func TestDispatchDeclinedUpgradeAborts(t *testing.T) {
	f := newDispatchFixture(t, "v0.3.2", "env", "list")
	f.d.executable = writeExecutable(t, t.TempDir(), "skali", fakeCLI("v0.3.2"))
	feed := f.serveFeed(t, "v0.4.0")
	f.seedRemote(t, "khz", fakeDaemon(t, "v0.4.0").URL, "v0.4.0")
	f.consent(t, false)

	handled, code := f.d.run()
	require.True(t, handled)
	require.Equal(t, 1, code)
	require.Empty(t, f.spawns)
	require.Empty(t, feed.requested())
	require.Contains(t, f.stderr.String(), "error: upgrade declined; run skali upgrade --version v0.4.0 to manage remote khz")
	unchanged, err := os.ReadFile(f.d.executable)
	require.NoError(t, err)
	require.Equal(t, fakeCLI("v0.3.2"), unchanged)
}

func TestDispatchOfflineRefusesNewerRecord(t *testing.T) {
	f := newDispatchFixture(t, "v0.3.2", "validate", "--offline")
	f.d.executable = writeExecutable(t, t.TempDir(), "skali", fakeCLI("v0.3.2"))
	f.seedRemote(t, "khz", deadURL(t), "v0.4.0")
	f.seedCache(t, "v0.4.0")
	f.consent(t, true)

	handled, code := f.d.run()
	require.True(t, handled)
	require.Equal(t, 1, code)
	require.Empty(t, f.spawns, "a cached newer release is not run behind a stale home")
	require.Contains(t, f.stderr.String(), "error: remote khz recorded skali v0.4.0, newer than this CLI v0.3.2; run skali upgrade --version v0.4.0 first")
}

func TestRerunAfterMismatch(t *testing.T) {
	oldArgs := os.Args
	os.Args = []string{"skali", "env", "list"}
	t.Cleanup(func() { os.Args = oldArgs })
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

	// A released remote that moved past home: no rerun, the pending skew
	// hint names the upgrade instead.
	skew.record("khz", "v0.5.0")
	handled, _ = rerunAfterMismatch(mismatch, none, &stderr, false, run)
	require.False(t, handled, "home never runs a newer release")
	require.Zero(t, calls)
	require.Empty(t, stderr.String())

	// A released remote that moved below home: one hint, then the
	// dispatcher runs.
	skew.record("khz", "v0.3.0")
	handled, code := rerunAfterMismatch(mismatch, none, &stderr, false, run)
	require.True(t, handled)
	require.Equal(t, 0, code)
	require.Equal(t, 1, calls)
	require.Equal(t, "hint: remote khz now runs skalid v0.3.0; rerunning with skali v0.3.0\n", stderr.String())

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

func TestRerunRefusesUpwardMove(t *testing.T) {
	// Home v0.6.0 dispatches down to a v0.4.0 record; the child finds the
	// cluster already at v0.7.0. The rerun never upgrades home behind the
	// user's back: it stops and names the upgrade.
	f := newDispatchFixture(t, "v0.6.0", "env", "list")
	f.d.executable = writeExecutable(t, t.TempDir(), "skali", fakeCLI("v0.6.0"))
	f.seedCache(t, "v0.4.0")
	f.serveFeed(t, "v0.7.0")
	f.seedRemote(t, "khz", fakeDaemon(t, "v0.4.0").URL, "v0.4.0")
	f.script = []func(spawnCall) (childStatus, error){
		func(spawnCall) (childStatus, error) {
			cfg := loadConfig(t)
			cfg.Remotes["khz"].Version = "v0.7.0"
			seedConfig(t, cfg)
			return childStatus{Code: exitVersionMoved}, nil
		},
	}

	handled, code := f.d.run()
	require.True(t, handled)
	require.Equal(t, 1, code)
	require.Len(t, f.spawns, 1)
	require.Contains(t, f.stderr.String(), "error: remote khz now runs skali v0.7.0, newer than this CLI v0.6.0; run skali upgrade --version v0.7.0 and run the command again")
	installed, err := os.ReadFile(f.d.executable)
	require.NoError(t, err)
	require.Equal(t, fakeCLI("v0.6.0"), installed, "home is untouched")
	_, cached := installer.CachedBinary(installer.CLICachePath(f.cache, "v0.7.0"))
	require.False(t, cached, "nothing is fetched")
}

func TestRerunFollowsDownwardMove(t *testing.T) {
	// The same rerun below home fetches the release the record moved to
	// and runs the command once more, as before.
	f := newDispatchFixture(t, "v0.6.0", "env", "list")
	f.seedCache(t, "v0.4.0")
	f.serveFeed(t, "v0.5.0")
	f.seedRemote(t, "khz", fakeDaemon(t, "v0.4.0").URL, "v0.4.0")
	f.script = []func(spawnCall) (childStatus, error){
		func(spawnCall) (childStatus, error) {
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
	require.Len(t, f.spawns, 2)
	require.Equal(t, installer.CLICachePath(f.cache, "v0.5.0"), f.spawns[1].path)
	require.NotContains(t, f.stderr.String(), "upgraded skali")
}

func TestHandoffRefusesNewerReleaseNonInteractive(t *testing.T) {
	f := newDispatchFixture(t, "v0.3.2", "remote", "add", "khz", "https://khz.example/api")
	f.d.executable = writeExecutable(t, t.TempDir(), "skali", fakeCLI("v0.3.2"))
	feed := f.serveFeed(t, "v0.4.0")

	handled, code := f.d.handoff(context.Background(), "khz", deadURL(t), "v0.4.0")
	require.True(t, handled)
	require.Equal(t, 1, code)
	require.Empty(t, f.spawns)
	require.Empty(t, feed.requested())
	require.Contains(t, f.stderr.String(), "error: remote khz runs skali v0.4.0, newer than this CLI v0.3.2; run skali upgrade --version v0.4.0 first")
}

func TestHandoffUpgradesHomeOnConsentAndRuns(t *testing.T) {
	f := newDispatchFixture(t, "v0.3.2", "remote", "add", "khz", "https://khz.example/api")
	f.d.executable = writeExecutable(t, t.TempDir(), "skali", fakeCLI("v0.3.2"))
	f.serveFeed(t, "v0.4.0")
	f.consent(t, true)

	handled, code := f.d.handoff(context.Background(), "khz", deadURL(t), "v0.4.0")
	require.True(t, handled)
	require.Equal(t, 0, code)
	require.Len(t, f.spawns, 1)
	require.Equal(t, installer.CLICachePath(f.cache, "v0.4.0"), f.spawns[0].path)
	require.Equal(t, []string{"remote", "add", "khz", "https://khz.example/api"}, f.spawns[0].args, "the child starts the command over")
	installed, err := os.ReadFile(f.d.executable)
	require.NoError(t, err)
	require.Equal(t, fakeCLI("v0.4.0"), installed, "home was upgraded")
	require.Contains(t, f.stderr.String(), "fetching skali v0.4.0 for remote khz")
	require.Contains(t, f.stderr.String(), "upgraded skali v0.3.2 -> v0.4.0 (remote khz runs skalid v0.4.0)")
}

func TestHandoffRunsCachedOlderRelease(t *testing.T) {
	f := newDispatchFixture(t, "v0.5.0", "remote", "login", "khz")
	path := f.seedCache(t, "v0.4.0")

	handled, code := f.d.handoff(context.Background(), "khz", deadURL(t), "v0.4.0")
	require.True(t, handled)
	require.Equal(t, 0, code)
	require.Len(t, f.spawns, 1)
	require.Equal(t, path, f.spawns[0].path)
	require.Empty(t, f.stderr.String(), "downward dispatch from the cache prints nothing")
}

func TestHandoffSkips(t *testing.T) {
	ctx := context.Background()

	f := newDispatchFixture(t, "v0.4.0", "--verbose", "remote", "status")
	handled, _ := f.d.handoff(ctx, "khz", deadURL(t), "v0.4.0")
	require.False(t, handled, "same release")
	require.Empty(t, f.spawns)
	require.Contains(t, f.stderr.String(), `dispatch: remote khz runs skalid "v0.4.0", this skali is v0.4.0`)

	f = newDispatchFixture(t, "v0.0.0-dev", "--verbose", "remote", "status")
	handled, _ = f.d.handoff(ctx, "khz", deadURL(t), "v0.4.0")
	require.False(t, handled, "development home")
	require.Contains(t, f.stderr.String(), "dispatch: skipped (development build v0.0.0-dev)")

	f = newDispatchFixture(t, "v0.5.0", "--verbose", "remote", "status")
	f.d.env = envMap(map[string]string{envDispatched: "1"})
	handled, _ = f.d.handoff(ctx, "khz", deadURL(t), "v0.4.0")
	require.False(t, handled, "a child never hands off again")
	require.Empty(t, f.stderr.String(), "the front gate already named the reason for a child")

	f = newDispatchFixture(t, "v0.5.0", "--verbose", "remote", "status")
	f.d.env = envMap(map[string]string{envNoDispatch: "1"})
	handled, _ = f.d.handoff(ctx, "khz", deadURL(t), "v0.4.0")
	require.False(t, handled, "the off switch holds")
	require.Contains(t, f.stderr.String(), "dispatch: skipped (SKALI_NO_DISPATCH is set)")
}

func TestHandoffProbesEmptyVersion(t *testing.T) {
	f := newDispatchFixture(t, "v0.5.0", "remote", "status")
	daemon := fakeDaemon(t, "v0.4.0")
	path := f.seedCache(t, "v0.4.0")

	handled, code := f.d.handoff(context.Background(), "khz", daemon.URL, "")
	require.True(t, handled)
	require.Equal(t, 0, code)
	require.Len(t, f.spawns, 1)
	require.Equal(t, path, f.spawns[0].path)

	f = newDispatchFixture(t, "v0.5.0", "remote", "status")
	handled, _ = f.d.handoff(context.Background(), "khz", deadURL(t), "")
	require.True(t, handled, "an unreachable daemon fails closed")
	require.Empty(t, f.spawns)
	require.Contains(t, f.stderr.String(), "error:")
}

func TestHandoffFetchFailureFailsClosed(t *testing.T) {
	f := newDispatchFixture(t, "v0.5.0", "remote", "add", "khz", "khz.example") // the fixture's feed is a dead URL

	handled, _ := f.d.handoff(context.Background(), "khz", deadURL(t), "v0.4.0")
	require.True(t, handled)
	require.Empty(t, f.spawns)
	require.Contains(t, f.stderr.String(), "error: could not fetch skali v0.4.0 from the release feed: ")
	require.NotContains(t, f.stderr.String(), "; running")
}

func TestHandoffLeftover213IsAnError(t *testing.T) {
	f := newDispatchFixture(t, "v0.5.0", "remote", "add", "khz", "khz.example")
	f.seedCache(t, "v0.4.0")
	f.script = []func(spawnCall) (childStatus, error){
		func(spawnCall) (childStatus, error) { return childStatus{Code: exitVersionMoved}, nil },
	}

	handled, code := f.d.handoff(context.Background(), "khz", deadURL(t), "v0.4.0")
	require.True(t, handled)
	require.Equal(t, 1, code, "a remote being added has no record to rerun from")
	require.Len(t, f.spawns, 1)
	require.Contains(t, f.stderr.String(), "error: remote khz changed its release while the command ran; run it again")
}

func TestPassthroughExit(t *testing.T) {
	_, ok := passthroughExit(nil)
	require.False(t, ok)
	_, ok = passthroughExit(errors.New("boom"))
	require.False(t, ok)
	code, ok := passthroughExit(&client.ExecExitError{Code: 7})
	require.True(t, ok)
	require.Equal(t, 7, code)
	code, ok = passthroughExit(fmt.Errorf("wrapped: %w", &dispatchedExit{code: 3}))
	require.True(t, ok)
	require.Equal(t, 3, code)
}

// A version skew recorded from the local platform never turns a dispatched
// child's failure into exit 213: skali dev owns that platform and switches
// it itself; only a remote's refusal asks the parent to rerun.
func TestRefusedAsWrongReleaseIgnoresLocal(t *testing.T) {
	t.Cleanup(skew.reset)
	withCLIVersion(t, "v1.0.0")
	skew.record(localRemoteName, "v9.9.9")
	require.False(t, refusedAsWrongRelease(errors.New("boom")))
	skew.record("khz", "v9.9.9")
	require.False(t, refusedAsWrongRelease(errors.New("boom")))
	require.True(t, refusedAsWrongRelease(&client.APIError{Code: client.CodeCLIVersionMismatch}))
}
