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

func TestUpgradeHint(t *testing.T) {
	withCLIVersion(t, "v0.2.0")
	hint := upgradeHint("ghcr.io/hinkolas/skalid:v0.1.0")
	require.Contains(t, hint, "v0.1.0")
	require.Contains(t, hint, "v0.2.0")
	require.Contains(t, hint, "skali dev upgrade")

	// Current or ahead stays quiet; ahead is the CLI's problem.
	require.Empty(t, upgradeHint("ghcr.io/hinkolas/skalid:v0.2.0"))
	require.Empty(t, upgradeHint("ghcr.io/hinkolas/skalid:v0.3.0"))
	// A prerelease of the CLI's version is behind it.
	require.Contains(t, upgradeHint("ghcr.io/hinkolas/skalid:v0.2.0-rc.1"), "v0.2.0-rc.1")
	// Non-published platforms have no comparable version.
	require.Empty(t, upgradeHint("skalid:dev"))
	require.Empty(t, upgradeHint("registry.example.com/skalid:v0.1.0"))
}

func TestUpgradeHintDevBuildCLI(t *testing.T) {
	withCLIVersion(t, "v0.0.0-dev")
	require.Empty(t, upgradeHint("ghcr.io/hinkolas/skalid:v0.1.0"))
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
