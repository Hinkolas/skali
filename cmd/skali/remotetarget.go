package main

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/Hinkolas/skali/internal/checkout"
	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/manifest"
)

var errNoRemote = errors.New("no remote selected; run skali remote add <name> <url> or skali remote use <name>")

// remoteTarget is the remote an invocation talks to, resolved without
// building a client: the --remote override first (the checkout binding is
// ignored, so Binding is nil), the binding's master when a manifest is
// discovered at or above start, the current remote otherwise. Inspection
// commands and the dispatcher share this ladder so the binary that runs a
// command is the one for the remote the command will talk to.
type remoteTarget struct {
	Name    string
	Remote  *cliconfig.Remote
	Binding *checkout.Target
}

// unboundCheckoutError is a checkout binding whose master no remote on
// this machine names; selecting it is an error.
type unboundCheckoutError struct{ Master string }

func (e *unboundCheckoutError) Error() string {
	return fmt.Sprintf("no remote for %s on this machine; run skali remote add <name> %s", e.Master, e.Master)
}

// resolveRemoteTarget applies the ladder. manifestPath is an explicit
// --manifest (empty discovers skali.yml upward from start); the override
// is also the only way to reach the dev-owned local remote, which is never
// current.
func resolveRemoteTarget(cfg *cliconfig.Config, manifestPath, start, override string) (*remoteTarget, error) {
	if frozen := invocationContext; frozen != nil && frozen.Resolved && frozen.Remote == "" && override == "" {
		return nil, errNoRemote
	}
	if frozen := invocationContext; frozen != nil && frozen.Remote != "" && (override == "" || override == frozen.Remote) {
		remote, err := remoteByName(cfg, frozen.Remote)
		if err != nil {
			return nil, err
		}
		if remote.Master != frozen.Master || remote.Instance != frozen.Instance {
			return nil, fmt.Errorf("remote %s changed during this command; run it again", frozen.Remote)
		}
		return &remoteTarget{Name: frozen.Remote, Remote: remote, Binding: frozen.Binding}, nil
	}
	if override != "" {
		remote, err := remoteByName(cfg, override)
		if err != nil {
			return nil, err
		}
		return &remoteTarget{Name: override, Remote: remote}, nil
	}
	var binding *checkout.Target
	if path, err := manifest.Discover(manifestPath, start); err == nil {
		if binding, err = checkout.Load(filepath.Dir(path)); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, manifest.ErrNotFound) {
		return nil, err
	}
	if binding != nil {
		name, found, ok := lookupRemoteByMaster(cfg, binding.Master)
		if !ok {
			return nil, &unboundCheckoutError{Master: binding.Master}
		}
		return &remoteTarget{Name: name, Remote: found, Binding: binding}, nil
	}
	if cfg.CurrentRemote == "" {
		return nil, errNoRemote
	}
	name, remote, err := cfg.Current()
	if err != nil {
		return nil, err
	}
	return &remoteTarget{Name: name, Remote: remote}, nil
}
