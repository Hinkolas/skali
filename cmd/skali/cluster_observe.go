package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/client"
	versionpkg "github.com/Hinkolas/skali/internal/version"
)

var newObservationDispatcher func([]string) (*dispatcher, error)

func init() { newObservationDispatcher = newDispatcher }

// Only observation crosses the release boundary. The accepted operation ID
// replaces all submission arguments, including --version, --yes and --recover.
func handoffUpdateObservation(ctx context.Context, api *client.Client, id string) error {
	selected := invocationContext
	if selected == nil || selected.Remote == "" || selected.Master != api.Master() || selected.Handoffs >= 2 {
		return fmt.Errorf("daemon release changed while observing update %s; inspect cluster status", id)
	}
	cfg, err := cliconfig.Load()
	if err != nil {
		return err
	}
	target, err := resolveRemoteTarget(cfg, "", "", selected.Remote)
	if err != nil {
		return err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	probe := remoteClient(cfg, target.Remote)
	if err := probe.Health(probeCtx); err != nil {
		return err
	}
	release := probe.ObservedVersion()
	if !versionpkg.IsRelease(release) || lacksCommand("cluster upgrade", release) || release == versionpkg.Version {
		return fmt.Errorf("cannot observe update %s with daemon release %q", id, release)
	}
	return runUpdateObserver(ctx, selected, release, id)
}

// Kept separate from network resolution so the handoff's arguments and
// operation identity can be exercised without submitting an update.
func runUpdateObserver(ctx context.Context, selected *versionContext, release, id string) error {
	d, err := newObservationDispatcher([]string{"cluster", "upgrade", "--observe-operation", id, "--remote", selected.Remote})
	if err != nil {
		return err
	}
	next := *selected
	next.Release, next.Mode, next.Handoffs = release, "verified", selected.Handoffs+1
	d.selected = &next
	if selected.Home != "" {
		d.executable = selected.Home
	}
	fetch, err := d.ensureCLI(ctx, selected.Remote, release)
	if err != nil {
		return err
	}
	d.promoteHome(ctx, fetch.binary, release, selected.Remote)
	fmt.Fprintf(os.Stderr, "Continuing observation of update %s with skali %s.\n", id, release)
	_, code := d.runLoop(ctx, selected.Remote, release, fetch.path)
	return &dispatchedExit{code: code}
}
