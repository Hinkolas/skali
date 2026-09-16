package main

import (
	"fmt"
	"os"

	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/filelock"
	"github.com/Hinkolas/skali/internal/installer"
	versionpkg "github.com/Hinkolas/skali/internal/version"
	"github.com/spf13/cobra"
)

// Platform status never downloads a CLI or contacts a remote cluster. Project
// inspection may use a cached CLI for the platform already installed here.
func dispatchDevInspection(command *cobra.Command, release string) error {
	if release == versionpkg.Version || release == "" && !versionpkg.IsRelease(versionpkg.Version) {
		return nil
	}
	cache := installer.DefaultCacheDir()
	path := installer.CLICachePath(cache, release)
	if !versionpkg.IsRelease(release) {
		fmt.Fprintln(command.OutOrStdout(), "project    use a working-tree CLI to inspect this platform")
		return &dispatchedExit{0}
	}
	unlock, err := filelock.Shared(command.Context(), installer.CLILockPath(cache, release))
	if err != nil {
		return err
	}
	defer unlock()
	if _, ok := installer.CachedBinary(path); !ok {
		fmt.Fprintf(command.OutOrStdout(), "project    skali %s is not cached; run skali dev with its target to fetch it\n", release)
		return &dispatchedExit{0}
	}
	cfg, err := cliconfig.Load()
	if err != nil {
		return err
	}
	remote := cfg.Remotes[localRemoteName]
	if remote == nil {
		fmt.Fprintln(command.OutOrStdout(), "project    local login is missing; run skali dev")
		return &dispatchedExit{0}
	}
	selected := &versionContext{Remote: localRemoteName, Master: remote.Master, Instance: remote.Instance, Release: release, Source: "installed local platform", Mode: "offline"}
	status, err := spawnChild(command.Context(), path, []string{"dev", "status", "--project-only"}, contextEnvironment(os.Environ(), selected))
	if err != nil {
		return err
	}
	return &dispatchedExit{status.Code}
}
