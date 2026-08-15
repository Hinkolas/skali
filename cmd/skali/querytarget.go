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

var errEnvironmentRequired = errors.New("--environment is required (or run inside a checkout linked by skali deploy)")

// queryTarget is the resolved read-only context of an inspection command:
// the remote to query and the environment to query it about.
type queryTarget struct {
	remoteName    string
	master        string
	api           *client.Client
	project       string
	environment   string
	environmentID string
}

// queryProject is the resolved project scope of a project-wide read
// (backup ls): the remote to query and the project with its current
// environments.
type queryProject struct {
	remoteName   string
	api          *client.Client
	project      *client.Project
	environments []client.Environment
	// binding is the checkout binding the scope came from; nil without one
	// or with an explicit remote override.
	binding *checkout.Target
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
			return "", nil, nil, fmt.Errorf("no remote for %s on this machine; run skali remote add <name> %s",
				binding.Master, binding.Master)
		}
		remoteName, remote = name, found
	} else if remoteName, remote, err = cfg.Current(); err != nil {
		return "", nil, nil, err
	}
	return remoteName, binding, remoteClient(cfg, remote), nil
}

// resolveQueryProject resolves the remote and project that project-wide
// reads work on. The checkout binding discovered at or above start names
// the project; without a binding (or with an explicit remote override) the
// checkout's manifest names it, since deploy always creates the project
// under the manifest name. Outside any checkout an environment must be
// named and the project holding it is found by scanning the remote.
// Nothing is ever created or linked.
func resolveQueryProject(ctx context.Context, start, environment, remote string) (*queryProject, error) {
	remoteName, binding, api, err := resolveQueryRemote(start, remote)
	if err != nil {
		return nil, err
	}
	scope := &queryProject{remoteName: remoteName, api: api, binding: binding}
	projectName := ""
	if binding != nil {
		projectName = binding.Project
	} else if projectName, err = checkoutProjectName(start); err != nil {
		return nil, err
	}
	if projectName != "" {
		project, err := findProject(ctx, api, projectName)
		if err != nil {
			return nil, err
		}
		if project == nil {
			return nil, fmt.Errorf("project %s does not exist on %s", projectName, api.Master())
		}
		scope.project = project
		if scope.environments, err = api.ListEnvironments(ctx, project.ID); err != nil {
			return nil, err
		}
		return scope, nil
	}
	if environment == "" {
		return nil, errEnvironmentRequired
	}
	if scope.project, scope.environments, err = projectOfEnvironment(ctx, api, environment); err != nil {
		return nil, err
	}
	return scope, nil
}

// checkoutProjectName is the project name of the manifest discovered at or
// above start, or empty outside a checkout.
func checkoutProjectName(start string) (string, error) {
	path, err := manifest.Discover("", start)
	if err != nil {
		return "", nil
	}
	document, err := manifest.ParseFile(path)
	if err != nil {
		return "", err
	}
	return document.Project.Name, nil
}

// resolveQueryTarget resolves the remote and environment that inspection
// commands (run list, logs) read from. The checkout binding discovered at
// or above start supplies the defaults, an explicit environment overrides
// the bound one for a single invocation, and nothing is ever created or
// linked. Without a binding (or with an explicit remote override) the
// environment must be named explicitly.
func resolveQueryTarget(ctx context.Context, start, environment, remote string) (*queryTarget, error) {
	scope, err := resolveQueryProject(ctx, start, environment, remote)
	if err != nil {
		return nil, err
	}
	if environment == "" && scope.binding != nil {
		environment = scope.binding.Environment
	}
	if environment == "" {
		return nil, errEnvironmentRequired
	}
	resolved := findEnvironment(scope.environments, environment)
	if resolved == nil {
		return nil, fmt.Errorf("environment %s does not exist in project %s on %s",
			environment, scope.project.Name, scope.api.Master())
	}
	return &queryTarget{
		remoteName:    scope.remoteName,
		master:        scope.api.Master(),
		api:           scope.api,
		project:       scope.project.Name,
		environment:   environment,
		environmentID: resolved.ID,
	}, nil
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

// projectOfEnvironment resolves an environment name without a binding by
// scanning every project on the remote, returning the project that holds
// it along with all of that project's environments.
func projectOfEnvironment(ctx context.Context, api *client.Client, environment string) (*client.Project, []client.Environment, error) {
	projects, err := api.ListProjects(ctx)
	if err != nil {
		return nil, nil, err
	}
	for i := range projects {
		environments, err := api.ListEnvironments(ctx, projects[i].ID)
		if err != nil {
			return nil, nil, err
		}
		if findEnvironment(environments, environment) != nil {
			return &projects[i], environments, nil
		}
	}
	return nil, nil, fmt.Errorf("environment %s not found on this installation", environment)
}
