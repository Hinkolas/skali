package main

import (
	"testing"

	versionpkg "github.com/Hinkolas/skali/internal/version"
)

// withCLIVersion stamps this test binary as the given release for the
// duration of a test; development builds are exempt from every version
// rule, so release behavior needs a release-shaped version.
func withCLIVersion(t *testing.T, version string) {
	t.Helper()
	original := versionpkg.Version
	versionpkg.Version = version
	t.Cleanup(func() { versionpkg.Version = original })
}
