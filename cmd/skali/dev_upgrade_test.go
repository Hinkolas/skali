package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	versionpkg "github.com/Hinkolas/skali/internal/version"
)

func withCLIVersion(t *testing.T, version string) {
	t.Helper()
	original := versionpkg.Version
	versionpkg.Version = version
	t.Cleanup(func() { versionpkg.Version = original })
}

func TestPublishedSkalidVersion(t *testing.T) {
	version, ok := versionpkg.PublishedSkalidVersion("ghcr.io/hinkolas/skalid:v0.1.0")
	require.True(t, ok)
	require.Equal(t, "v0.1.0", version)

	// Prereleases are published too.
	version, ok = versionpkg.PublishedSkalidVersion("ghcr.io/hinkolas/skalid:v0.1.0-rc.1")
	require.True(t, ok)
	require.Equal(t, "v0.1.0-rc.1", version)

	for _, image := range []string{
		"skalid:dev",
		"ghcr.io/hinkolas/skalid:latest",
		"ghcr.io/hinkolas/skalid:v0.1.0-3-gabc1234",
		"ghcr.io/hinkolas/skalid:v0.1.0-dirty",
		"registry.example.com/skalid:v0.1.0",
		"",
	} {
		_, ok := versionpkg.PublishedSkalidVersion(image)
		require.False(t, ok, image)
	}
}

func TestUpgradeTargetPrereleaseCLI(t *testing.T) {
	withCLIVersion(t, "v0.2.0-rc.1")
	t.Chdir(t.TempDir())
	image, _, err := upgradeTarget()
	require.NoError(t, err)
	require.Equal(t, "ghcr.io/hinkolas/skalid:v0.2.0-rc.1", image)
}

func TestUpgradeTargetReleasedCLI(t *testing.T) {
	withCLIVersion(t, "v0.2.0")
	// Outside the repository (findRepoRoot walks up from the working
	// directory, so run from a temp dir), a released CLI names its
	// published image.
	t.Chdir(t.TempDir())
	image, repoRoot, err := upgradeTarget()
	require.NoError(t, err)
	require.Empty(t, repoRoot)
	require.Equal(t, "ghcr.io/hinkolas/skalid:v0.2.0", image)
}

func TestUpgradeTargetDevBuildOutsideRepo(t *testing.T) {
	withCLIVersion(t, "v0.0.0-dev")
	t.Chdir(t.TempDir())
	_, _, err := upgradeTarget()
	require.ErrorContains(t, err, "--skalid-image")
}

func TestUpgradeTargetWorkingTree(t *testing.T) {
	// Inside the repository the working-tree build wins regardless of the
	// CLI version; the test binary runs from cmd/skali, inside it.
	withCLIVersion(t, "v0.2.0")
	image, repoRoot, err := upgradeTarget()
	require.NoError(t, err)
	require.NotEmpty(t, repoRoot)
	require.Equal(t, "skalid:dev", image)
}
