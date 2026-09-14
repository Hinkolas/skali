package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/filelock"
	"github.com/Hinkolas/skali/internal/installer"
	versionpkg "github.com/Hinkolas/skali/internal/version"
)

// Self-dispatch (docs/versioning.md, decision 1). The API between skali and
// skalid is a private wire built from one commit, so the CLI that talks to
// a cluster is always that cluster's exact release. Every skali binary is
// the dispatcher: it looks up the version the target remote last answered
// with, and when that differs from its own it runs the cached binary of
// that release as a child, fetching it from the release feed first when
// needed. Home, the binary in PATH, is promoted to the newest
// version in use, so dispatch normally goes downward.

const (
	// exitVersionMoved is the reserved exit status a dispatched child ends
	// with when the daemon turned out to run another release than the one
	// it was dispatched for: the parent re-reads the record and runs the
	// command once more with the right binary.
	exitVersionMoved = 213
	// envDispatched marks a child so it never dispatches again.
	envDispatched = "SKALI_DISPATCHED"
	// envNoDispatch turns dispatch off for a user who manages versions by
	// hand; the daemon's gate and the skew hint then speak.
	envNoDispatch = "SKALI_NO_DISPATCH"
)

// dispatchTried records that this process went as far as fetching another
// release for the command (whether or not that worked), so a later refusal
// of the in-process run does not start a second fetch.
var dispatchTried bool

// dispatcher carries every environmental input of one dispatch decision so
// tests can substitute them.
type dispatcher struct {
	args        []string
	env         func(string) string
	environ     func() []string
	homeVersion string // the running binary's version, never mutated
	installed   string // the version on disk at executable; moves with a promotion
	executable  string
	cacheDir    string
	cwd         string
	home        string // user home, for completion refresh after promotion
	goos        string
	goarch      string
	releaseBase string
	feedClient  *http.Client
	stderr      io.Writer
	root        func() *cobra.Command
	spawn       func(ctx context.Context, path string, args, env []string) (childStatus, error)
	selected    *versionContext
	now         func() time.Time
}

func newDispatcher(args []string) (*dispatcher, error) {
	executable, err := locateExecutable()
	if err != nil {
		return nil, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	home, _ := os.UserHomeDir()
	return &dispatcher{
		args:        args,
		env:         dispatchEnvironment,
		environ:     os.Environ,
		homeVersion: versionpkg.Version,
		installed:   versionpkg.Version,
		executable:  executable,
		cacheDir:    installer.DefaultCacheDir(),
		cwd:         cwd,
		home:        home,
		goos:        runtime.GOOS,
		goarch:      runtime.GOARCH,
		releaseBase: releaseBase(),
		feedClient:  &http.Client{Timeout: 5 * time.Minute},
		stderr:      os.Stderr,
		root:        newRootCommand,
		spawn:       spawnChild,
		now:         time.Now,
	}, nil
}

// dispatch is the first thing main does. handled reports that the command
// ran in a child (code is the status to exit with); otherwise the caller
// runs it in this process.
func dispatch(args []string) (handled bool, code int) {
	frozen, err := contextFromEnvironment()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return true, 1
	}
	if frozen != nil {
		invocationContext = frozen
		return false, 0
	}
	d, err := newDispatcher(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return true, 1
	}
	// A shell launched by a worker is not itself that worker.
	d.env = func(key string) string {
		if key == envDispatched {
			return ""
		}
		return os.Getenv(key)
	}
	handled, code = d.run()
	invocationContext = d.selected
	return handled, code
}

// dispatchGate names the reason a command line never dispatches, or ""
// when the dispatcher should look at the remote. Cheapest checks first;
// nothing here touches the config or the network.
func dispatchGate(inv invocation, env func(string) string, home, executable, cacheDir string) string {
	if reason := handoffGate(env, home, executable, cacheDir); reason != "" {
		return reason
	}
	switch {
	case inv.version:
		return "help"
	case inv.command == "":
		return "no command"
	case homeInvocation(inv):
		return inv.command + " always runs at home"

	}
	return ""
}

// handoffGate is the part of the gate that holds for any command line: the
// process and binary conditions under which this binary never runs
// another release.
func handoffGate(env func(string) string, home, executable, cacheDir string) string {
	switch {
	case env(envDispatched) != "":
		return "already dispatched"
	case env(envNoDispatch) != "":
		return envNoDispatch + " is set"
	case !versionpkg.IsRelease(home):
		return "development build " + home
	}
	return ""
}

// dispatchTarget decides whether a recorded daemon version needs another
// binary than home: only a release that differs.
func dispatchTarget(home, record string) (string, bool) {
	if !versionpkg.ReleasesDiffer(home, record) {
		return "", false
	}
	return record, true
}

// insideDir reports whether path lies below dir, both with symlinks
// resolved (macOS keeps temp dirs behind /var -> /private/var).
func insideDir(path, dir string) bool {
	if dir == "" {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return strings.HasPrefix(filepath.Clean(path), filepath.Clean(dir)+string(filepath.Separator))
}

func (d *dispatcher) note(inv invocation, format string, args ...any) {
	if inv.verbose {
		fmt.Fprintf(d.stderr, "dispatch: "+format+"\n", args...)
	}
}

func (d *dispatcher) style() *clirender.Style {
	return clirender.StyleFor(d.stderr)
}

func (d *dispatcher) failure(err error) (bool, int) {
	fmt.Fprintln(d.stderr, "error:", err)
	return true, 1
}

func (d *dispatcher) run() (bool, int) {
	inv := preparseArgs(d.args, d.root)
	if reason := dispatchGate(inv, d.env, d.homeVersion, d.executable, d.cacheDir); reason != "" {
		d.note(inv, "skipped (%s)", reason)
		d.selected = &versionContext{Release: d.homeVersion, Source: "this CLI", Mode: "home"}
		return false, 0
	}
	if inv.help {
		return d.help(inv)
	}
	cfg, err := cliconfig.Load()
	if err != nil {
		return d.failure(err)
	}
	target, err := resolveRemoteTarget(cfg, inv.manifest, d.cwd, inv.remote)
	if err != nil {
		// No configured target is a supported local-only workflow; malformed
		// bindings and explicit selections never fall through to home.
		if errors.Is(err, errNoRemote) {
			d.selected = &versionContext{Resolved: true, Release: d.homeVersion, Source: "this CLI", Mode: "home"}
			if !inv.completion && inv.command != "skill" {
				fmt.Fprintf(d.stderr, "using skali %s (home; no target)\n", d.homeVersion)
			}
			return false, 0
		}
		if inv.completion {
			return true, 0
		}
		return d.failure(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	source := "current remote"
	if target.Binding != nil {
		source = "checkout binding"
	}
	if inv.remote != "" {
		source = "--remote"
	}
	mode := "verified"
	record := target.Remote.Version
	if inv.offline || inv.completion {
		mode = "offline"
		if !versionpkg.IsRelease(record) {
			if inv.completion {
				return true, 0
			}
			return d.failure(fmt.Errorf("remote %s has no recorded release; connect once without --offline", target.Name))
		}
	} else {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		probe := client.New(target.Remote.Master, "", caller())
		probe.PinInstance(target.Remote.Instance, nil)
		err := probe.Health(probeCtx)
		cancel()
		if err != nil {
			hint := "check the remote address and cluster availability"
			if supportsOffline(inv) {
				hint = "local workflows can use --offline with a recorded release"
			}
			return d.failure(fmt.Errorf("verify remote %s: %w; %s", target.Name, err, hint))
		}
		record = probe.ObservedVersion()
		if !versionpkg.IsRelease(record) {
			return d.failure(fmt.Errorf("remote %s does not advertise a supported release (%q); use a working-tree CLI for a development daemon", target.Name, record))
		}
		if err := cliconfig.Observe(target.Name, *target.Remote, probe.ObservedInstance(), record); err != nil {
			return d.failure(err)
		}
		if observed := probe.ObservedInstance(); observed != "" {
			target.Remote.Instance = observed
		}
	}
	if unsupportedDispatchRelease(record) {
		if inv.completion {
			return true, 0
		}
		return d.failure(fmt.Errorf("remote %s runs unsupported prerelease %s; this versioning contract starts at %s", target.Name, record, minimumDispatchRelease))
	}
	d.selected = &versionContext{Resolved: true, Home: d.executable, Remote: target.Name, Master: target.Remote.Master, Instance: target.Remote.Instance, Release: record, Source: source, Mode: mode, Binding: target.Binding}
	if !inv.completion && inv.command != "skill" {
		fmt.Fprintf(d.stderr, "target %s: skali %s (%s, %s)\n", target.Name, record, source, mode)
	}
	want, different := dispatchTarget(d.homeVersion, record)
	if !different {
		return false, 0
	}
	dispatchTried = true
	var fetch *cliFetch
	if inv.offline || inv.completion {
		path := installer.CLICachePath(d.cacheDir, want)
		data, ok := installer.CachedBinary(path)
		if !ok {
			if inv.completion {
				return true, 0
			}
			return d.failure(fmt.Errorf("skali %s is not cached; connect once without --offline", want))
		}
		fetch = &cliFetch{version: want, path: path, binary: data}
	} else {
		fetch, err = d.ensureCLI(ctx, target.Name, want)
		if err != nil {
			if ctx.Err() != nil {
				return true, 130
			}
			return d.failure(err)
		}
	}
	if !inv.completion && versionpkg.Older(d.installed, fetch.version) {
		d.promoteHome(ctx, fetch.binary, fetch.version, target.Name)
	}
	handled, code := d.runLoop(ctx, target.Name, fetch.version, fetch.path)
	if fetch.fetched {
		if current, err := cliconfig.Load(); err == nil {
			pruneCLICache(current, d.installed, d.cacheDir)
		}
	}
	return handled, code
}

// handoff runs the current command line in the release a command's own
// probe named. remote add, remote login and remote status learn their
// target inside the command (the front gate cannot: the remote may not
// exist yet), so they call this once the daemon has answered and before
// any output or prompt; the child starts over and does the work at the
// cluster's release. An empty version costs one health probe.
func (d *dispatcher) handoff(ctx context.Context, remoteName, master, version string) (handled bool, code int) {
	inv := preparseArgs(d.args, d.root)
	if reason := handoffGate(d.env, d.homeVersion, d.executable, d.cacheDir); reason != "" {
		if d.env(envDispatched) == "" {
			// The front gate already named this reason for a child.
			d.note(inv, "skipped (%s)", reason)
		}
		return false, 0
	}
	cfg, err := cliconfig.Load()
	if err != nil {
		return d.failure(err)
	}
	instance := ""
	if remote := cfg.Remotes[remoteName]; remote != nil {
		if remote.Master != master {
			return d.failure(fmt.Errorf("remote %s changed; run again", remoteName))
		}
		instance = remote.Instance
	}
	if version == "" {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		probe := client.New(master, "", caller())
		probe.PinInstance(instance, nil)
		if err := probe.Health(probeCtx); err != nil {
			return d.failure(err)
		}
		version = probe.ObservedVersion()
	}
	if !versionpkg.IsRelease(version) {
		return d.failure(fmt.Errorf("remote %s does not advertise a supported release (%q)", remoteName, version))
	}
	d.selected = &versionContext{Resolved: true, Home: d.executable, Remote: remoteName, Master: master, Instance: instance, Release: version, Source: "remote command", Mode: "verified"}

	if versionpkg.IsRelease(version) && unsupportedDispatchRelease(version) {
		return d.failure(fmt.Errorf("unsupported prerelease %s; supported releases start at %s", version, minimumDispatchRelease))
	}
	want, ok := dispatchTarget(d.homeVersion, version)
	if !ok {
		d.note(inv, "remote %s runs skalid %q, this skali is %s", remoteName, version, d.homeVersion)
		return false, 0
	}
	dispatchTried = true
	fetch, err := d.ensureCLI(ctx, remoteName, want)
	if err != nil {
		if ctx.Err() != nil {
			return true, 130
		}
		return d.failure(err)
	}
	if versionpkg.Older(d.installed, fetch.version) {
		d.promoteHome(ctx, fetch.binary, fetch.version, remoteName)
	}
	if fetch.fetched {
		if cfg, err := cliconfig.Load(); err == nil {
			pruneCLICache(cfg, d.installed, d.cacheDir)
		}
	}
	d.note(inv, "running skali %s for remote %s", fetch.version, remoteName)
	handled, code = d.runLoop(ctx, remoteName, fetch.version, fetch.path)
	if handled && code == exitVersionMoved {
		// The cluster changed release between the probe and the command,
		// and the child said nothing (a remote being added has no record
		// for the rerun to read). None of the commands that hand off pass
		// a remote process's status through, so 213 is never their own.
		fmt.Fprintln(d.stderr, d.style().BoldRed("error:"), fmt.Sprintf("remote %s changed its release while the command ran; run it again", remoteName))
		return true, 1
	}
	return handled, code
}

// dispatchedExit is the error a command returns when handoff ran it in
// another release: main exits with the child's code and prints nothing.
type dispatchedExit struct{ code int }

func (e *dispatchedExit) Error() string {
	return fmt.Sprintf("command ran in another skali release (exit %d)", e.code)
}

// dispatchTo is what remote add, remote login and remote status call once
// their probe has named the cluster's release; nil means the command goes
// on in this process. A variable so command tests can observe the call,
// assigned in init because its body reaches the command tree that calls it.
var dispatchTo func(ctx context.Context, remoteName, master, version string) error

func init() { dispatchTo = runHandoff }

func runHandoff(ctx context.Context, remoteName, master, version string) error {
	d, err := newDispatcher(os.Args[1:])
	if err != nil {
		return err
	}
	if handled, code := d.handoff(ctx, remoteName, master, version); handled {
		return &dispatchedExit{code: code}
	}
	if d.selected != nil {
		invocationContext = d.selected
	}
	return nil
}

// runLoop runs the command in the cached binary and, once, again in the
// release the record moved to while it ran.
func (d *dispatcher) runLoop(ctx context.Context, remoteName, want, path string) (bool, int) {
	inv := preparseArgs(d.args, d.root)
	for attempt := 0; ; attempt++ {
		// Hold the cache lease until the child exits. Publication and pruning use
		// this same lock; the worker never acquires it or dispatches recursively.
		unlock, err := filelock.Shared(ctx, installer.CLILockPath(d.cacheDir, want))
		if err != nil {
			return d.failure(err)
		}
		if _, valid := installer.CachedBinary(path); !valid {
			unlock()
			if inv.offline || inv.completion {
				return d.failure(fmt.Errorf("cached skali %s is no longer available", want))
			}
			fetch, err := d.ensureCLI(ctx, remoteName, want)
			if err != nil {
				return d.failure(err)
			}
			path = fetch.path
			unlock, err = filelock.Shared(ctx, installer.CLILockPath(d.cacheDir, want))
			if err != nil {
				return d.failure(err)
			}
		}
		status, err := d.spawn(ctx, path, d.args, contextEnvironment(d.environ(), d.selected))
		unlock()
		if err != nil {
			return d.failure(fmt.Errorf("cached skali %s could not start: %w", want, err))
		}
		if inv.completion {
			return true, d.finish(status)
		}
		if status.Code == exitVersionMoved && attempt > 0 && readOnlyInvocation(inv) {
			return d.failure(fmt.Errorf("remote %s changed release again; run the command again", remoteName))
		}
		if status.Code != exitVersionMoved || !readOnlyInvocation(inv) {
			return true, d.finish(status)
		}
		_, moved, ok := d.recordMoved(remoteName, want)
		if !ok {
			return d.failure(fmt.Errorf("remote %s refused skali %s, but its new release could not be recorded; run the command again", remoteName, want))
		}
		if inv.offline {
			return d.failure(fmt.Errorf("selected release changed; run the command again online"))
		}
		fetch, err := d.ensureCLI(ctx, remoteName, moved)
		if err != nil {
			return d.failure(err)
		}
		if d.selected != nil {
			d.selected.Release = moved
		}
		if versionpkg.Older(d.installed, moved) {
			d.promoteHome(ctx, fetch.binary, moved, remoteName)
		}
		want, path = moved, fetch.path
	}
}

// recordMoved reloads the config the child wrote and reports the release
// the remote's record now names when it is another one than the child ran.
func (d *dispatcher) recordMoved(remoteName, ran string) (*cliconfig.Config, string, bool) {
	cfg, err := cliconfig.Load()
	if err != nil {
		return nil, "", false
	}
	remote := cfg.Remotes[remoteName]
	if remote == nil || d.selected != nil && (remote.Master != d.selected.Master || remote.Instance != d.selected.Instance) || !versionpkg.IsRelease(remote.Version) || remote.Version == ran {
		return nil, "", false
	}
	return cfg, remote.Version, true
}

// finish turns a child's ending into this process's: a signal death is
// re-raised so the shell sees the same termination, anything else is the
// code to exit with.
func (d *dispatcher) finish(status childStatus) int {
	if status.Signal != 0 {
		reraise(status.Signal)
	}
	return status.Code
}

// promoteHome makes a fetched newer release the binary in PATH (home is
// the newest version in use). It never changes what runs next: the cached
// copy is executed either way, so the just-written home path is never
// execed by the dispatcher itself. Every outcome is one stderr line. The
// running process keeps its own version; installed tracks the disk.
func (d *dispatcher) promoteHome(ctx context.Context, binary []byte, target, remoteName string) bool {
	if insideDir(d.executable, d.cacheDir) {
		return false
	}
	style := d.style()
	dir := filepath.Dir(d.executable)
	if err := probeWritableDir(dir); err != nil {
		sudo := "sudo "
		if os.Geteuid() == 0 {
			sudo = ""
		}
		fmt.Fprintln(d.stderr, style.Yellow(fmt.Sprintf("hint: skali %s is cached but %s is not writable; run %sskali upgrade --version %s once to make it the default",
			target, dir, sudo, target)))
		return false
	}
	unlock, err := filelock.Acquire(ctx, d.executable+".lock")
	if err != nil {
		fmt.Fprintln(d.stderr, "warning: cannot lock home CLI:", err)
		return false
	}
	defer unlock()
	installed, err := installedCLIVersion(ctx, d.executable)
	if err != nil {
		fmt.Fprintln(d.stderr, "warning: cannot inspect home CLI:", err)
		return false
	}
	d.installed = installed
	if !versionpkg.IsRelease(installed) || !versionpkg.Older(installed, target) {
		return false
	}
	if err := installCLIUnlocked(ctx, d.executable, binary, target); err != nil {
		fmt.Fprintln(d.stderr, style.Yellow(fmt.Sprintf("warning: could not replace %s with skali %s: %v; running the cached copy",
			d.executable, target, err)))
		return false
	}
	if warning := refreshCompletions(ctx, clirender.NewTasks(io.Discard), d.executable, d.home); warning != "" {
		fmt.Fprintln(d.stderr, "warning:", warning)
	}
	if warning := refreshSkill(ctx, clirender.NewTasks(io.Discard), d.executable, d.home); warning != "" {
		fmt.Fprintln(d.stderr, "warning:", warning)
	}
	fmt.Fprintf(d.stderr, "upgraded skali %s -> %s (remote %s runs skalid %s)\n", d.installed, target, remoteName, target)
	d.installed = target
	return true
}

// rerunAfterMismatch covers the cluster that moved while home matched its
// record: the command ran in this binary, the daemon refused it as the
// wrong release, and remoteClient has just recorded the new version. The
// dispatcher can now fetch that release and run the command again, the
// same single rerun a dispatched child gets. Nothing happens in a child
// (its parent reruns), with dispatch turned off, or for a refusal that did
// not come from a named release remote, and not when this process already
// tried to dispatch; the caller prints the error and skew hint. Only
// explicitly read-only work can reach run, which is injected for tests.
func rerunAfterMismatch(err error, env func(string) string, stderr io.Writer, tried bool, run func() (bool, int)) (bool, int) {
	if !readOnlyInvocation(preparseArgs(os.Args[1:], newRootCommand)) {
		return false, 0
	}
	if tried || env(envDispatched) != "" || env(envNoDispatch) != "" {
		return false, 0
	}
	api, ok := errors.AsType[*client.APIError](err)
	if !ok || api.Code != client.CodeCLIVersionMismatch {
		return false, 0
	}
	remote, server := skew.snapshot()
	if remote == "" || remote == localRemoteName || !versionpkg.ReleasesDiffer(versionpkg.Version, server) {
		return false, 0
	}
	fmt.Fprintln(stderr, clirender.StyleFor(stderr).Yellow(fmt.Sprintf("hint: remote %s now runs skalid %s; rerunning with skali %s",
		remote, server, server)))
	return run()
}

// exitCodeFor is the child side of the protocol: a dispatched binary the
// daemon refused as the wrong release exits exitVersionMoved so the parent
// reruns the command with the release the record now names. Every other
// failure is the usual 1.
func exitCodeFor(err error) int {
	if err == nil {
		return 0
	}
	if dispatchEnvironment(envDispatched) != "" && readOnlyInvocation(preparseArgs(os.Args[1:], newRootCommand)) && refusedAsWrongRelease(err) {
		return exitVersionMoved
	}
	return 1
}

// refusedAsWrongRelease recognizes only the daemon's typed version refusal.
// Observing another release on an unrelated failure never permits replay.
func refusedAsWrongRelease(err error) bool {
	api, ok := errors.AsType[*client.APIError](err)
	return ok && api.Code == client.CodeCLIVersionMismatch
}
