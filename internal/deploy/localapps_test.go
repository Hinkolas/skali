package deploy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/artifactstore"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/store"
)

const localBuildManifest = `version: "1"
name: demo
applications:
  web:
    build:
      context: .
    ports:
      http:
        port: 8080
        protocol: http
`

func webLocal() map[string]LocalApplication {
	return map[string]LocalApplication{"web": {Ports: map[string]int32{"http": 5173}}}
}

func TestPlanLocalApplicationSkipsBuildInput(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	definitionVersion := f.submit(t, localBuildManifest, 0)

	// Without the intercept the build-sourced app demands its hashes.
	_, err := f.deploy.PlanPreview(ctx, PlanInput{
		EnvironmentID:       f.environmentID,
		DefinitionVersionID: definitionVersion,
	})
	var missing *MissingBuildInputError
	require.ErrorAs(t, err, &missing)

	// Intercepted: no build input, no action, no artifact for the app.
	preview, err := f.deploy.PlanPreview(ctx, PlanInput{
		EnvironmentID:       f.environmentID,
		DefinitionVersionID: definitionVersion,
		LocalApplications:   webLocal(),
	})
	require.NoError(t, err)
	require.Empty(t, preview.Actions)
	require.NotContains(t, preview.Candidate.Artifacts, "web")
}

func TestLocalApplicationValidation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	definitionVersion := f.submit(t, localBuildManifest, 0)

	// Managed clusters reject interception outright.
	_, err := f.deploy.PlanPreview(ctx, PlanInput{
		EnvironmentID:       f.environmentID,
		DefinitionVersionID: definitionVersion,
		LocalApplications:   webLocal(),
		ManagedCluster:      true,
	})
	var unsupported *LocalApplicationsUnsupportedError
	require.ErrorAs(t, err, &unsupported)

	// Unknown application key.
	_, err = f.deploy.PlanPreview(ctx, PlanInput{
		EnvironmentID:       f.environmentID,
		DefinitionVersionID: definitionVersion,
		LocalApplications:   map[string]LocalApplication{"ghost": {}},
	})
	var unknown *UnknownLocalApplicationError
	require.ErrorAs(t, err, &unknown)

	// Declared ports must cover the rendered service ports.
	_, err = f.deploy.PlanPreview(ctx, PlanInput{
		EnvironmentID:       f.environmentID,
		DefinitionVersionID: definitionVersion,
		LocalApplications:   map[string]LocalApplication{"web": {}},
	})
	var invalid *InvalidInterceptPortsError
	require.ErrorAs(t, err, &invalid)
	require.Contains(t, invalid.Error(), "http")
}

func TestPromoteReplacesAndClearsIntercepts(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	jsvc := journal.NewService(f.st, "executor-local")
	definitionVersion := f.submit(t, testManifest, 0)
	f.stageCurrent(t)

	// Deploy with the intercept: the row set is written at promotion.
	result, err := f.deploy.Execute(ctx, ExecuteInput{
		ProjectID:           f.projectID,
		EnvironmentID:       f.environmentID,
		DefinitionVersionID: definitionVersion,
		Resolver:            &artifactstore.Fake{Store: f.artifacts, ProjectID: f.projectID},
		Journal:             jsvc,
		Actor:               "tester",
		LocalApplications:   webLocal(),
	})
	require.NoError(t, err)
	rows, err := f.st.ListEnvironmentIntercepts(ctx, f.environmentID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "web", rows[0].ApplicationKey)
	require.JSONEq(t, `{"http":5173}`, string(rows[0].Ports))

	// The intercepted revision records no artifact for the app.
	revision, err := f.deploy.GetRevision(ctx, result.RevisionID)
	require.NoError(t, err)
	require.NotContains(t, revision.Artifacts, "web")

	// A deploy without local applications clears the set.
	_, err = f.deploy.Execute(ctx, ExecuteInput{
		ProjectID:           f.projectID,
		EnvironmentID:       f.environmentID,
		DefinitionVersionID: definitionVersion,
		Resolver:            &artifactstore.Fake{Store: f.artifacts, ProjectID: f.projectID},
		Journal:             jsvc,
		Actor:               "tester",
	})
	require.NoError(t, err)
	rows, err = f.st.ListEnvironmentIntercepts(ctx, f.environmentID)
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestInterceptChangeDefeatsUpToDate(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	jsvc := journal.NewService(f.st, "executor-uptodate")
	definitionVersion := f.submit(t, testManifest, 0)
	f.stageCurrent(t)

	result, err := f.deploy.Execute(ctx, ExecuteInput{
		ProjectID:           f.projectID,
		EnvironmentID:       f.environmentID,
		DefinitionVersionID: definitionVersion,
		Resolver:            &artifactstore.Fake{Store: f.artifacts, ProjectID: f.projectID},
		Journal:             jsvc,
		Actor:               "tester",
		LocalApplications:   webLocal(),
	})
	require.NoError(t, err)
	rows, err := f.st.SetEnvironmentActiveRevision(ctx, store.SetEnvironmentActiveRevisionParams{
		EnvironmentID:    f.environmentID,
		ActiveRevisionID: &result.RevisionID,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)

	// Same intercept set: up to date.
	preview, err := f.deploy.PlanPreview(ctx, PlanInput{
		EnvironmentID:       f.environmentID,
		DefinitionVersionID: definitionVersion,
		LocalApplications:   webLocal(),
	})
	require.NoError(t, err)
	require.True(t, preview.UpToDate)

	// A ports-only change keeps the checksum but must open a real window.
	preview, err = f.deploy.PlanPreview(ctx, PlanInput{
		EnvironmentID:       f.environmentID,
		DefinitionVersionID: definitionVersion,
		LocalApplications:   map[string]LocalApplication{"web": {Ports: map[string]int32{"http": 4321}}},
	})
	require.NoError(t, err)
	require.False(t, preview.UpToDate)

	// Clearing the intercept is a change too, even though nothing else moved.
	// (The checksum differs here as well, since the artifact returns, but the
	// intercept comparison alone must already force the window.)
	changed, err := f.deploy.interceptsChanged(ctx, f.environmentID, nil)
	require.NoError(t, err)
	require.True(t, changed)
}

// stageCurrent promotes a minimal value set so testManifest's required
// variables resolve from current values in intercept tests.
func (f *fixture) stageCurrent(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	candidate := f.stage(t,
		map[string]string{"APP_DOMAIN": "demo.example.com", "SESSION_SECRET": "intercept-plant-value"})
	require.NoError(t, f.st.WithTx(ctx, func(q *store.Queries) error {
		return f.values.PromoteTx(ctx, q, f.environmentID, candidate.ID)
	}))
}
