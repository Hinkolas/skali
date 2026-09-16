package main

import (
	"fmt"
	"github.com/Hinkolas/skali/internal/localdev"
)

// The resolver already froze the target; never infer it from newer config.
func devVersionReason() string {
	release := localdev.PlatformVersion()
	if release == "" {
		release = "working tree"
	}
	return fmt.Sprintf("local platform %s runs %s; %s", localdev.ClusterName(), release, versionDescription())
}
