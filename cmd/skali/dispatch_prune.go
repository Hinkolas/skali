package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/filelock"
	"github.com/Hinkolas/skali/internal/installer"
	versionpkg "github.com/Hinkolas/skali/internal/version"
)

// pruneGrace protects a cache entry another skali may have just fetched
// and not yet recorded or started.
const pruneGrace = 10 * time.Minute

// referencedReleases is the set of releases something on this machine
// uses: home and every remote's recorded version. The dev-owned local
// remote counts for the CLI cache (the binary driving the running local
// platform must stay cached) and not for local platforms themselves (its
// version only mirrors whichever platform last logged in).
func referencedReleases(cfg *cliconfig.Config, home string, includeLocal bool) map[string]bool {
	keep := map[string]bool{}
	if home != "" {
		keep[home] = true
	}
	if cfg != nil {
		for name, remote := range cfg.Remotes {
			if remote.Version != "" && (includeLocal || name != localRemoteName) {
				keep[remote.Version] = true
			}
		}
	}
	return keep
}

// pruneCLICache removes cached releases nothing references: not this
// binary's version and not the recorded version of any remote. It runs
// after a fetch stored a new entry, after a promotion, and when a remote
// is removed or home upgraded, the moments a reference moves on. Removal
// renames the entry aside first so a half-deleted directory is never
// mistaken for a valid one; unlinking a binary a running child still
// executes is safe. Best effort throughout.
func pruneCLICache(cfg *cliconfig.Config, home, cacheDir string) {
	keep := referencedReleases(cfg, home, true)
	dir := installer.CLICacheDir(cacheDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	now := time.Now()
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".trash-") {
			_ = os.RemoveAll(filepath.Join(dir, name))
			continue
		}
		if !entry.IsDir() || !versionpkg.IsRelease(name) || keep[name] {
			continue
		}
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) < pruneGrace {
			continue
		}
		unlock, err := filelock.Try(installer.CLILockPath(cacheDir, name))
		if err != nil || unlock == nil {
			continue
		}
		trash := filepath.Join(dir, fmt.Sprintf(".trash-%s-%d", name, os.Getpid()))
		if err := os.Rename(filepath.Join(dir, name), trash); err != nil {
			unlock()
			continue
		}
		_ = os.RemoveAll(trash)
		unlock()
	}
}
