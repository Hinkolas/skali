package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/filelock"
	"github.com/Hinkolas/skali/internal/installer"
	versionpkg "github.com/Hinkolas/skali/internal/version"
)

// Help is local inspection: never probe, fetch, promote, or replay a command.
// A matching cached release is preferred, but home help remains a recovery path.
func (d *dispatcher) help(inv invocation) (bool, int) {
	home := func(reason string) (bool, int) {
		d.selected = &versionContext{Release: d.homeVersion, Source: "this CLI", Mode: "home"}
		fmt.Fprintf(d.stderr, "help from skali %s (home; %s); target compatibility is not verified\n", d.homeVersion, reason)
		return false, 0
	}
	cfg, err := cliconfig.Load()
	if err != nil {
		return home(err.Error())
	}
	target, err := resolveRemoteTarget(cfg, inv.manifest, d.cwd, inv.remote)
	if err != nil {
		return home(err.Error())
	}
	release := target.Remote.Version
	if !versionpkg.IsRelease(release) || unsupportedDispatchRelease(release) {
		return home(fmt.Sprintf("target %s has no supported recorded release", target.Name))
	}
	source := "current remote"
	if target.Binding != nil {
		source = "checkout binding"
	}
	if inv.remote != "" {
		source = "--remote"
	}
	d.selected = &versionContext{Resolved: true, Home: d.executable, Remote: target.Name, Master: target.Remote.Master, Instance: target.Remote.Instance, Release: release, Source: source, Mode: "offline", Binding: target.Binding}
	note := func() {
		fmt.Fprintf(d.stderr, "help from skali %s (target %s; %s; recorded release, not verified online)\n", release, target.Name, source)
	}
	if release == d.homeVersion {
		note()
		return false, 0
	}
	ctx := context.Background()
	// A concurrent download must not make help wait for the network indirectly.
	unlock, err := filelock.TryShared(installer.CLILockPath(d.cacheDir, release))
	if err != nil {
		return home(err.Error())
	}
	if unlock == nil {
		return home(fmt.Sprintf("cached skali %s is being updated", release))
	}
	defer unlock()
	path := installer.CLICachePath(d.cacheDir, release)
	if _, valid := installer.CachedBinary(path); !valid {
		return home(fmt.Sprintf("target %s requires skali %s, which is not available in cache", target.Name, release))
	}
	status, err := d.spawn(ctx, path, helpArgs(d.args), contextEnvironment(d.environ(), d.selected))
	if err != nil {
		return home(fmt.Sprintf("cached skali %s could not start: %v", release, err))
	}
	note()
	return true, d.finish(status)
}

func supportsOffline(inv invocation) bool {
	return inv.command == "dev" || inv.path == "validate" || inv.path == "compile" || inv.path == "manifest upgrade" || inv.path == "skill read"
}

// --offline is redundant for help. Accept it without adding an offline mode to
// API commands such as deploy. Ordinary invocations retain normal flag errors.
func helpArgs(args []string) []string {
	if !preparseArgs(args, newRootCommand).help {
		return args
	}
	result := make([]string, 0, len(args))
	for i, arg := range args {
		if arg == "--" {
			return append(result, args[i:]...)
		}
		if arg == "--offline" || strings.HasPrefix(arg, "--offline=") {
			continue
		}
		result = append(result, arg)
	}
	return result
}
