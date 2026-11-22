package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	versionpkg "github.com/Hinkolas/skali/internal/version"
)

// envFlagRelease is the first release that spells the environment flag
// --env. Earlier releases only know --environment.
const envFlagRelease = "v0.1.4-rc.4"

// renamedFlags maps flags that are gone to the spelling that replaced them.
var renamedFlags = map[string]string{
	"--environment": "--env",
}

// flagError names the new spelling when a renamed flag is passed, so the
// error says what to type instead of only that the flag is unknown.
func flagError(_ *cobra.Command, err error) error {
	for old, current := range renamedFlags {
		if err.Error() == "unknown flag: "+old {
			return fmt.Errorf("%s was renamed to %s", old, current)
		}
	}
	return err
}

// argsForRelease rewrites a command line for the release that runs it: a
// release older than envFlagRelease is handed --environment for --env.
// Arguments after a literal -- belong to the command and stay untouched.
func argsForRelease(args []string, release string) []string {
	if !versionpkg.IsRelease(release) || !versionpkg.Older(release, envFlagRelease) {
		return args
	}
	rewritten := make([]string, len(args))
	copy(rewritten, args)
	for i, arg := range rewritten {
		if arg == "--" {
			break
		}
		if arg == "--env" {
			rewritten[i] = "--environment"
		} else if value, ok := strings.CutPrefix(arg, "--env="); ok {
			rewritten[i] = "--environment=" + value
		}
	}
	return rewritten
}
