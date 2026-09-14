package main

import (
	"fmt"
	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/localdev"
)

// The resolver already froze the target; never infer it from newer config.
func devVersionReason(_ *cliconfig.Config, _, _, _ string) string {
	release := localdev.PlatformVersion()
	if release == "" {
		release = "working tree"
	}
	return fmt.Sprintf("local platform %s runs %s; %s", localdev.ClusterName(), release, versionDescription())
}
