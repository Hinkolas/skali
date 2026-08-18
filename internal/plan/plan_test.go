package plan

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/manifest"
	"github.com/Hinkolas/skali/internal/revision"
)

const baseManifest = `
version: "1"
name: demo
applications:
  web:
    image: example.invalid/web:1
    environment:
      SESSION_SECRET: "${SESSION_SECRET}"
databases:
  data:
    engine: postgres
    version: 17
`

func digest(fill string) string {
	return "sha256:" + strings.Repeat(fill, 64)
}

func buildRevision(t *testing.T, source string, mutate func(*revision.Input)) *revision.Revision {
	t.Helper()
	document, err := manifest.Parse([]byte(source), "skali.yml")
	require.NoError(t, err)
	result, err := compiler.Compile(document)
	require.NoError(t, err)
	input := revision.Input{
		Result:         result,
		Environment:    "production",
		SecretVersions: map[string]int{"SESSION_SECRET": 1},
		Artifacts: map[string]revision.Artifact{
			"web": {Reference: "registry.internal/demo/web", Digest: digest("1"), Kind: revision.KindImport},
		},
		CompilerVersion: "test",
	}
	if mutate != nil {
		mutate(&input)
	}
	built, err := revision.Build(input)
	require.NoError(t, err)
	return built
}

func changeFor(t *testing.T, result *Plan, service string) Change {
	t.Helper()
	for _, change := range result.Changes {
		if change.Service == service {
			return change
		}
	}
	t.Fatalf("plan has no change for %s", service)
	return Change{}
}

func TestInitialDeploymentCreatesEverything(t *testing.T) {
	t.Parallel()
	candidate := buildRevision(t, baseManifest, nil)
	result := Diff(nil, candidate)

	require.Equal(t, Change{Service: "applications.web", Action: ActionCreate}, changeFor(t, result, "applications.web"))
	require.Equal(t, Change{Service: "databases.data", Action: ActionCreate}, changeFor(t, result, "databases.data"))
	require.Equal(t, []ValueChange{{Name: "SESSION_SECRET", Action: ActionCreate}}, result.Values)
	require.False(t, result.Destructive())
}

func TestUnchangedRevisionPlansNothing(t *testing.T) {
	t.Parallel()
	active := buildRevision(t, baseManifest, nil)
	candidate := buildRevision(t, baseManifest, nil)
	result := Diff(active, candidate)
	require.True(t, result.Empty())
}

func TestDatabaseKeyRenameIsDestructive(t *testing.T) {
	t.Parallel()
	active := buildRevision(t, baseManifest, nil)
	renamed := strings.Replace(baseManifest, "  data:", "  main:", 1)
	candidate := buildRevision(t, renamed, nil)

	result := Diff(active, candidate)
	removed := changeFor(t, result, "databases.data")
	require.Equal(t, ActionRemove, removed.Action)
	require.True(t, removed.Destructive)
	require.Contains(t, removed.Detail, "deletes the logical database")
	require.Equal(t, ActionCreate, changeFor(t, result, "databases.main").Action)
	require.True(t, result.Destructive())
}

func TestArtifactChangeUpdatesTheApplication(t *testing.T) {
	t.Parallel()
	active := buildRevision(t, baseManifest, nil)
	candidate := buildRevision(t, baseManifest, func(input *revision.Input) {
		input.Artifacts["web"] = revision.Artifact{
			Reference: "registry.internal/demo/web", Digest: digest("2"), Kind: revision.KindImport,
		}
	})

	result := Diff(active, candidate)
	updated := changeFor(t, result, "applications.web")
	require.Equal(t, ActionUpdate, updated.Action)
	require.False(t, updated.Destructive)
	require.Equal(t, "artifact "+strings.Repeat("2", 12)+" replaces "+strings.Repeat("1", 12), updated.Detail)
}

func TestPendingArtifactPlansWithoutAHash(t *testing.T) {
	t.Parallel()
	active := buildRevision(t, baseManifest, nil)
	candidate := buildRevision(t, baseManifest, func(input *revision.Input) {
		input.Artifacts["web"] = revision.Artifact{
			Reference: "pending", Digest: revision.PendingDigest, Kind: revision.KindImport,
		}
	})

	result := Diff(active, candidate)
	updated := changeFor(t, result, "applications.web")
	require.Equal(t, ActionUpdate, updated.Action)
	require.Equal(t, "a new artifact replaces "+strings.Repeat("1", 12), updated.Detail)
}

func TestSecretVersionBumpIsAValueUpdate(t *testing.T) {
	t.Parallel()
	active := buildRevision(t, baseManifest, nil)
	candidate := buildRevision(t, baseManifest, func(input *revision.Input) {
		input.SecretVersions = map[string]int{"SESSION_SECRET": 2}
	})

	result := Diff(active, candidate)
	require.Empty(t, result.Changes)
	require.Equal(t, []ValueChange{{Name: "SESSION_SECRET", Action: ActionUpdate}}, result.Values)
}

func TestPruneAddsValueRowsWithoutTouchingDestructive(t *testing.T) {
	t.Parallel()
	active := buildRevision(t, baseManifest, nil)
	candidate := buildRevision(t, baseManifest, func(input *revision.Input) {
		input.SecretVersions = map[string]int{"SESSION_SECRET": 2}
	})

	result := Diff(active, candidate)
	result.Prune([]string{"ZZ_OLD", "AA_OLD"})
	// Sorted by name alongside the revision-level rows; prune is a store
	// change, never a destructive one.
	require.Equal(t, []ValueChange{
		{Name: "AA_OLD", Action: ActionPrune},
		{Name: "SESSION_SECRET", Action: ActionUpdate},
		{Name: "ZZ_OLD", Action: ActionPrune},
	}, result.Values)
	require.False(t, result.Destructive())
	require.False(t, result.Empty())

	// Pruning nothing changes nothing.
	unchanged := Diff(active, active)
	unchanged.Prune(nil)
	require.True(t, unchanged.Empty())
}

func TestVolumeRemovalIsDestructive(t *testing.T) {
	t.Parallel()
	withVolume := `
version: "1"
name: demo
applications:
  web:
    image: example.invalid/web:1
    volumes:
      cache:
        mountPath: /cache
        size: 1GB
`
	withoutVolume := `
version: "1"
name: demo
applications:
  web:
    image: example.invalid/web:1
`
	noSecrets := func(input *revision.Input) {
		input.SecretVersions = nil
	}
	active := buildRevision(t, withVolume, noSecrets)
	candidate := buildRevision(t, withoutVolume, noSecrets)

	result := Diff(active, candidate)
	updated := changeFor(t, result, "applications.web")
	require.Equal(t, ActionUpdate, updated.Action)
	require.True(t, updated.Destructive)
	require.Contains(t, updated.Detail, "deletes persistent volumes: cache")

	removal := Diff(active, buildRevision(t, `
version: "1"
name: demo
applications:
  other:
    image: example.invalid/other:1
`, func(input *revision.Input) {
		input.SecretVersions = nil
		input.Artifacts = map[string]revision.Artifact{
			"other": {Reference: "registry.internal/demo/other", Digest: digest("3"), Kind: revision.KindImport},
		}
	}))
	removed := changeFor(t, removal, "applications.web")
	require.Equal(t, ActionRemove, removed.Action)
	require.True(t, removed.Destructive)
	require.Contains(t, removed.Detail, "deletes persistent volumes")
}
