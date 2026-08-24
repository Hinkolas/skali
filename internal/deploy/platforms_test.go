package deploy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/artifactstore"
	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/store"
)

func TestSplitPlatforms(t *testing.T) {
	require.Nil(t, splitPlatforms(""))
	require.Nil(t, splitPlatforms(" , "))
	require.Equal(t, []string{"linux/amd64"}, splitPlatforms("linux/amd64"))
	require.Equal(t, []string{"linux/amd64", "linux/arm64"}, splitPlatforms("linux/arm64,linux/amd64"))
	require.Equal(t, []string{"linux/arm64"}, splitPlatforms(" linux/arm64 , linux/arm64 "))
}

func TestIntersectPlatforms(t *testing.T) {
	amd := []string{"linux/amd64"}
	both := []string{"linux/amd64", "linux/arm64"}
	require.Equal(t, both, intersectPlatforms(nil, both), "unknown declared keeps the cluster side")
	require.Equal(t, amd, intersectPlatforms(amd, nil), "unknown cluster keeps the declared side")
	require.Equal(t, amd, intersectPlatforms(amd, both))
	require.Empty(t, intersectPlatforms(amd, []string{"linux/arm64"}))
}

func TestArtifactPlatforms(t *testing.T) {
	imageSource := compiler.ApplicationSource{Kind: "image", Platforms: []string{"linux/amd64"}}
	require.Equal(t, []string{"linux/amd64"}, artifactPlatforms(imageSource, "ignored"))
	buildSource := compiler.ApplicationSource{Kind: "build", Platforms: []string{"linux/amd64", "linux/arm64"}}
	require.Equal(t, []string{"linux/arm64"}, artifactPlatforms(buildSource, "linux/arm64"),
		"a build's artifact records what it was built for, not what it could support")
	require.Nil(t, artifactPlatforms(compiler.ApplicationSource{Kind: "build"}, ""))
}

const declaredImageManifest = `version: "1"
name: demo
applications:
  web:
    image: ghcr.io/example/web:1.0.0
    platforms: [linux/arm64]
    ports:
      http:
        port: 8080
        protocol: http
`

const declaredBuildManifest = `version: "1"
name: demo
applications:
  api:
    build:
      context: .
    platforms: [linux/amd64]
`

// Declared platforms narrow the platform guard: a submitted build platform
// must overlap the declared set intersected with the cluster's application
// platforms, and an image source's declared set must overlap the cluster.
func TestPreviewEnforcesDeclaredPlatforms(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	buildVersion := f.submit(t, declaredBuildManifest, 0)
	both := []string{"linux/amd64", "linux/arm64"}

	// The cluster runs both platforms, so plain overlap would pass; the
	// declared amd64-only set must reject an arm64 build.
	_, err := f.deploy.PlanPreview(ctx, PlanInput{
		EnvironmentID: f.environmentID, DefinitionVersionID: buildVersion,
		BuildInputs:   map[string]BuildInput{"api": {InputHash: "hash", ConfigHash: "config", Platform: "linux/arm64"}},
		NodePlatforms: both,
	})
	var mismatch *PlatformMismatchError
	require.ErrorAs(t, err, &mismatch)
	require.Equal(t, "api", mismatch.Application)
	require.Equal(t, []string{"linux/amd64"}, mismatch.Declared)

	// Declared platforms bind even when the cluster platforms are unknown.
	_, err = f.deploy.PlanPreview(ctx, PlanInput{
		EnvironmentID: f.environmentID, DefinitionVersionID: buildVersion,
		BuildInputs: map[string]BuildInput{"api": {InputHash: "hash", ConfigHash: "config", Platform: "linux/arm64"}},
	})
	require.ErrorAs(t, err, &mismatch)

	// A matching build passes.
	preview, err := f.deploy.PlanPreview(ctx, PlanInput{
		EnvironmentID: f.environmentID, DefinitionVersionID: buildVersion,
		BuildInputs:   map[string]BuildInput{"api": {InputHash: "hash", ConfigHash: "config", Platform: "linux/amd64"}},
		NodePlatforms: both,
	})
	require.NoError(t, err)
	require.False(t, preview.UpToDate)

	// An image source whose declared platforms are disjoint from the
	// cluster would render an unschedulable workload: rejected at plan.
	imageVersion := f.submit(t, declaredImageManifest, 1)
	_, err = f.deploy.PlanPreview(ctx, PlanInput{
		EnvironmentID: f.environmentID, DefinitionVersionID: imageVersion,
		NodePlatforms: []string{"linux/amd64"},
	})
	require.ErrorAs(t, err, &mismatch)
	require.Equal(t, "web", mismatch.Application)
	require.Equal(t, []string{"linux/arm64"}, mismatch.Declared)
}

// The stored revision and the plan preview's candidate must derive
// identical artifact platforms, or an unchanged environment would never
// report up to date again.
func TestRevisionPlatformsMatchPreview(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	definitionVersion := f.submit(t, declaredImageManifest, 0)
	resolver := &artifactstore.Fake{Store: f.artifacts, ProjectID: f.projectID}
	prepared, err := f.deploy.Prepare(ctx, PrepareInput{
		EnvironmentID: f.environmentID, DefinitionVersionID: definitionVersion,
		Resolver: resolver,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"linux/arm64"}, prepared.Revision.Artifacts["web"].Platforms,
		"an image source's declared platforms land on the stored artifact")
	require.NoError(t, f.deploy.Promote(ctx, prepared))
	rows, err := f.st.SetEnvironmentActiveRevision(ctx, store.SetEnvironmentActiveRevisionParams{
		EnvironmentID: f.environmentID, ActiveRevisionID: &prepared.RevisionID,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)

	preview, err := f.deploy.PlanPreview(ctx, PlanInput{
		EnvironmentID: f.environmentID, DefinitionVersionID: definitionVersion,
		NodePlatforms: []string{"linux/arm64"},
	})
	require.NoError(t, err)
	require.True(t, preview.UpToDate,
		"the candidate revision must checksum-equal the stored one for identical inputs")
}

// Prepare stamps a build artifact with the platform recorded on the
// deployment's action, matching what the preview derived from the
// submitted build input.
func TestPrepareStampsActionPlatforms(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	definitionVersion := f.submit(t, declaredBuildManifest, 0)
	resolver := &artifactstore.Fake{Store: f.artifacts, ProjectID: f.projectID}
	prepared, err := f.deploy.Prepare(ctx, PrepareInput{
		EnvironmentID: f.environmentID, DefinitionVersionID: definitionVersion,
		Resolver:        resolver,
		ActionPlatforms: map[string]string{"api": "linux/amd64"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"linux/amd64"}, prepared.Revision.Artifacts["api"].Platforms)

	// Without a recorded platform the artifact stays unconstrained.
	bare, err := f.deploy.Prepare(ctx, PrepareInput{
		EnvironmentID: f.environmentID, DefinitionVersionID: definitionVersion,
		Resolver: resolver,
	})
	require.NoError(t, err)
	require.Nil(t, bare.Revision.Artifacts["api"].Platforms)
}
