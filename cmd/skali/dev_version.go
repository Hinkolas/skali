package main

import (
	"errors"
	"fmt"

	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/localdev"
	versionpkg "github.com/Hinkolas/skali/internal/version"
)

// devVersionReason says which release the local platform runs and why, in
// one line printed on every start (docs/versioning.md, decision 5). The
// release is this binary's: the dispatcher already walked the ladder
// (--remote, the checkout binding, the current remote, home) and ran the
// binary for it. This walks the same ladder again, cheaply, to name the
// rung; a rung that names another release means dispatch did not run.
func devVersionReason(cfg *cliconfig.Config, manifestPath, cwd, override string) string {
	cluster := localdev.ClusterName()
	release := localdev.PlatformVersion()
	if release == "" {
		return fmt.Sprintf("local platform runs the working tree (cluster %s)", cluster)
	}
	head := fmt.Sprintf("local platform runs skalid %s (cluster %s): ", release, cluster)
	target, err := resolveRemoteTarget(cfg, manifestPath, cwd, override)
	if err != nil {
		var unbound *unboundCheckoutError
		if errors.As(err, &unbound) {
			return head + fmt.Sprintf("this skali's release; .skali/target.yaml names %s but no remote here does", unbound.Master)
		}
		return head + "this skali's release"
	}
	source := "the current remote"
	switch {
	case override != "":
		source = "--remote"
	case target.Binding != nil:
		source = "from .skali/target.yaml"
	}
	record := target.Remote.Version
	switch {
	case record == release:
		return head + fmt.Sprintf("the release %s runs, %s", target.Name, source)
	case !versionpkg.IsRelease(record):
		return head + fmt.Sprintf("this skali's release (%s has not named a release yet)", target.Name)
	default:
		return head + fmt.Sprintf("this skali's release; %s runs skalid %s and dispatch did not run, see --verbose",
			target.Name, record)
	}
}
