package main

import (
	"fmt"
	"path/filepath"

	"github.com/Hinkolas/skali/internal/checkout"
	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/manifest"
)

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
// this machine names; the dispatcher runs home on it, dev says so.
type unboundCheckoutError struct{ Master string }

func (e *unboundCheckoutError) Error() string {
	return fmt.Sprintf("no remote for %s on this machine; run skali remote add <name> %s", e.Master, e.Master)
}

// resolveRemoteTarget applies the ladder. manifestPath is an explicit
// --manifest (empty discovers skali.yml upward from start); the override
// is also the only way to reach the dev-owned local remote, which is never
// current.
func resolveRemoteTarget(cfg *cliconfig.Config, manifestPath, start, override string) (*remoteTarget, error) {
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
	}
	if binding != nil {
		name, found, ok := lookupRemoteByMaster(cfg, binding.Master)
		if !ok {
			return nil, &unboundCheckoutError{Master: binding.Master}
		}
		return &remoteTarget{Name: name, Remote: found, Binding: binding}, nil
	}
	name, remote, err := cfg.Current()
	if err != nil {
		return nil, err
	}
	return &remoteTarget{Name: name, Remote: remote}, nil
}
