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
		env:         os.Getenv,
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
	d, err := newDispatcher(args)
	if err != nil {
		return false, 0
	}
	return d.run()
}

// dispatchGate names the reason a command line never dispatches, or ""
// when the dispatcher should look at the remote. Cheapest checks first;
// nothing here touches the config or the network.
func dispatchGate(inv invocation, env func(string) string, home, executable, cacheDir string) string {
	switch {
	case env(envDispatched) != "":
		return "already dispatched"
	case env(envNoDispatch) != "":
		return envNoDispatch + " is set"
	case !versionpkg.IsRelease(home):
		return "development build " + home
	case insideDir(executable, cacheDir):
		return "running from the cache"
	case inv.help || inv.version:
		return "help"
	case inv.command == "":
		return "no command"
	case noDispatchCommands[inv.command]:
		return inv.command + " always runs at home"
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

func (d *dispatcher) run() (bool, int) {
	inv := preparseArgs(d.args, d.root)
	if reason := dispatchGate(inv, d.env, d.homeVersion, d.executable, d.cacheDir); reason != "" {
		d.note(inv, "skipped (%s)", reason)
		return false, 0
	}
	cfg, err := cliconfig.Load()
	if err != nil {
		return false, 0
	}
	target, err := resolveRemoteTarget(cfg, inv.manifest, d.cwd, inv.remote)
	if err != nil {
		d.note(inv, "skipped (%v)", err)
		return false, 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	record := target.Remote.Version
	if record == "" {
		record = d.probeVersion(ctx, cfg, target)
	}
	want, ok := dispatchTarget(d.homeVersion, record)
	if !ok {
		d.note(inv, "remote %s runs skalid %q, this skali is %s", target.Name, record, d.homeVersion)
		return false, 0
	}
	dispatchTried = true
	fetch, err := d.ensureCLI(ctx, target.Name, want)
	if err != nil {
		if ctx.Err() != nil {
			return true, 130
		}
		d.warn(err)
		return false, 0
	}
	if versionpkg.Older(d.installed, fetch.version) {
		d.promoteHome(ctx, fetch.binary, fetch.version, target.Name)
	}
	if fetch.fetched {
		pruneCLICache(cfg, d.installed, d.cacheDir)
	}
	d.note(inv, "running skali %s for remote %s", fetch.version, target.Name)
	return d.runLoop(ctx, target.Name, fetch.version, fetch.path)
}

// probeVersion fills an empty record with one health round trip: the
// daemon's version rides the response headers pre-auth. Unreachable means
// no record and no dispatch; the command itself reports the outage.
func (d *dispatcher) probeVersion(ctx context.Context, cfg *cliconfig.Config, target *remoteTarget) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	probe := client.New(target.Remote.Master, "", caller())
	_ = probe.Health(ctx)
	observed := probe.ObservedVersion()
	if observed != "" {
		target.Remote.Version = observed
		_ = cliconfig.Save(cfg)
	}
	return observed
}

// runLoop runs the command in the cached binary and, once, again in the
// release the record moved to while it ran.
func (d *dispatcher) runLoop(ctx context.Context, remoteName, want, path string) (bool, int) {
	redispatched := false
	for {
		status, err := d.spawn(ctx, path, d.args, childEnv(d.environ()))
		if err != nil {
			fmt.Fprintln(d.stderr, d.style().Yellow(fmt.Sprintf("warning: cached skali %s could not start: %v; running skali %s",
				want, err, d.homeVersion)))
			if insideDir(path, d.cacheDir) {
				_ = os.RemoveAll(filepath.Dir(path))
			}
			return false, 0
		}
		if status.Code != exitVersionMoved || redispatched {
			return true, d.finish(status)
		}
		cfg, moved, ok := d.recordMoved(remoteName, want)
		if !ok {
			// 213 was the command's own status (skali exec passes the
			// remote process's code through); nothing moved.
			return true, d.finish(status)
		}
		redispatched = true
		if moved == d.homeVersion {
			fmt.Fprintln(d.stderr, d.style().Yellow(fmt.Sprintf("hint: remote %s now runs skalid %s; rerunning with this skali",
				remoteName, moved)))
			return false, 0
		}
		fetch, err := d.ensureCLI(ctx, remoteName, moved)
		if err != nil {
			if ctx.Err() != nil {
				return true, 130
			}
			d.warn(err)
			return false, 0
		}
		fmt.Fprintln(d.stderr, d.style().Yellow(fmt.Sprintf("hint: remote %s moved to skalid %s; rerunning with skali %s",
			remoteName, fetch.version, fetch.version)))
		if versionpkg.Older(d.installed, fetch.version) {
			d.promoteHome(ctx, fetch.binary, fetch.version, remoteName)
		}
		if fetch.fetched {
			pruneCLICache(cfg, d.installed, d.cacheDir)
		}
		want, path = fetch.version, fetch.path
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
	if remote == nil || !versionpkg.IsRelease(remote.Version) || remote.Version == ran {
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
	if err := installCLI(ctx, d.executable, binary, target); err != nil {
		fmt.Fprintln(d.stderr, style.Yellow(fmt.Sprintf("warning: could not replace %s with skali %s: %v; running the cached copy",
			d.executable, target, err)))
		return false
	}
	refreshCompletions(ctx, clirender.NewTasks(io.Discard), d.executable, d.home)
	fmt.Fprintf(d.stderr, "upgraded skali %s -> %s (remote %s runs skalid %s)\n", d.installed, target, remoteName, target)
	d.installed = target
	return true
}

// warn prints a fetch failure's one line; silent failures print nothing.
func (d *dispatcher) warn(err error) {
	var warning *dispatchWarning
	if errors.As(err, &warning) {
		fmt.Fprintln(d.stderr, d.style().Yellow("warning: "+warning.text))
	}
}

// rerunAfterMismatch covers the cluster that moved while home matched its
// record: the command ran in this binary, the daemon refused it as the
// wrong release, and remoteClient has just recorded the new version. The
// dispatcher can now fetch that release and run the command again, the
// same single rerun a dispatched child gets. Nothing happens in a child
// (its parent reruns), with dispatch turned off, or for a refusal that did
// not come from a named release remote, and not when this process already
// tried to dispatch (home ran because the fetch failed, and a warning said
// so); the caller then prints the error and the skew hint as usual. run is
// dispatch, injected for tests.
func rerunAfterMismatch(err error, env func(string) string, stderr io.Writer, tried bool, run func() (bool, int)) (bool, int) {
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
	if os.Getenv(envDispatched) != "" && refusedAsWrongRelease(err) {
		return exitVersionMoved
	}
	return 1
}

func refusedAsWrongRelease(err error) bool {
	if api, ok := errors.AsType[*client.APIError](err); ok && api.Code == client.CodeCLIVersionMismatch {
		return true
	}
	_, server := skew.snapshot()
	return versionpkg.ReleasesDiffer(versionpkg.Version, server)
}
