package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Hinkolas/skali/internal/checkout"
	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/manifest"
)

// queryTarget is the resolved read-only context of an inspection command:
// the remote to query and the environment to query it about.
type queryTarget struct {
	remoteName    string
	master        string
	api           *client.Client
	project       string // bound project name; empty without a binding
	environment   string
	environmentID string
}

// resolveQueryRemote picks the remote inspection commands talk to: an
// explicit --remote override first (the checkout binding is ignored, so
// the returned binding is nil), the binding's master when one is
// discovered at or above start, the current remote otherwise. The
// override is also the only way these commands reach the dev-owned local
// remote, which is never current.
func resolveQueryRemote(start, override string) (string, *checkout.Target, *client.Client, error) {
	cfg, err := cliconfig.Load()
	if err != nil {
		return "", nil, nil, err
	}
	if override != "" {
		remote, err := remoteByName(cfg, override)
		if err != nil {
			return "", nil, nil, err
		}
		return override, nil, remoteClient(cfg, remote), nil
	}
	var binding *checkout.Target
	if path, err := manifest.Discover("", start); err == nil {
		if binding, err = checkout.Load(filepath.Dir(path)); err != nil {
			return "", nil, nil, err
		}
	}
	var remoteName string
	var remote *cliconfig.Remote
	if binding != nil {
		name, found, ok := lookupRemoteByMaster(cfg, binding.Master)
		if !ok {
			return "", nil, nil, fmt.Errorf("no remote for %s on this machine; run skali remote add %s",
				binding.Master, binding.Master)
		}
		remoteName, remote = name, found
	} else if remoteName, remote, err = cfg.Current(); err != nil {
		return "", nil, nil, err
	}
	return remoteName, binding, remoteClient(cfg, remote), nil
}

// resolveQueryTarget resolves the remote and environment that inspection
// commands (run list, logs) read from. The checkout binding discovered at
// or above start supplies the defaults, an explicit environment overrides
// the bound one for a single invocation, and nothing is ever created or
// linked. Without a binding (or with an explicit remote override) the
// environment must be named explicitly.
func resolveQueryTarget(ctx context.Context, start, environment, remote string) (*queryTarget, error) {
	remoteName, binding, api, err := resolveQueryRemote(start, remote)
	if err != nil {
		return nil, err
	}

	if environment == "" && binding != nil {
		environment = binding.Environment
	}
	if environment == "" {
		return nil, errors.New("--environment is required (or run inside a checkout linked by skali deploy)")
	}

	target := &queryTarget{
		remoteName:  remoteName,
		master:      api.Master(),
		api:         api,
		environment: environment,
	}
	if binding != nil {
		project, err := findProject(ctx, api, binding.Project)
		if err != nil {
			return nil, err
		}
		if project == nil {
			return nil, fmt.Errorf("project %s does not exist on %s", binding.Project, api.Master())
		}
		environments, err := api.ListEnvironments(ctx, project.ID)
		if err != nil {
			return nil, err
		}
		resolved := findEnvironment(environments, environment)
		if resolved == nil {
			return nil, fmt.Errorf("environment %s does not exist in project %s on %s",
				environment, binding.Project, api.Master())
		}
		target.project = binding.Project
		target.environmentID = resolved.ID
		return target, nil
	}
	if target.environmentID, err = environmentIDByName(ctx, api, environment); err != nil {
		return nil, err
	}
	return target, nil
}

// queryClient is the binding-aware client for commands that carry their
// own scope (a run id): an explicit --remote override wins, then the
// checkout binding when one exists, the current remote otherwise.
func queryClient(remote string) (*client.Client, error) {
	start, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	_, _, api, err := resolveQueryRemote(start, remote)
	return api, err
}

// environmentIDByName resolves an environment name without a binding by
// scanning every project on the remote.
func environmentIDByName(ctx context.Context, api *client.Client, environment string) (string, error) {
	projects, err := api.ListProjects(ctx)
	if err != nil {
		return "", err
	}
	for _, project := range projects {
		environments, err := api.ListEnvironments(ctx, project.ID)
		if err != nil {
			return "", err
		}
		for _, candidate := range environments {
			if candidate.Name == environment {
				return candidate.ID, nil
			}
		}
	}
	return "", fmt.Errorf("environment %s not found on this installation", environment)
}
