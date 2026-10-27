package buildstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/build"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/redact"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/testdb"
)

func TestInsightsRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.New(t)
	st := store.NewStore(pool)
	svc := New(st)
	projects := project.New(st)
	proj, err := projects.Create(ctx, "demo", "", uuid.Nil)
	require.NoError(t, err)
	env, err := projects.CreateEnvironment(ctx, proj.ID, "production", project.EnvironmentOptions{})
	require.NoError(t, err)
	runs := journal.NewService(st, "executor-1")
	run, err := runs.CreateRun(ctx, journal.RunInput{
		Kind: "deployment", ProjectID: proj.ID, EnvironmentID: env.ID, Actor: "tester",
	})
	require.NoError(t, err)
	step, err := runs.EnsureStep(ctx, run.ID, nil, "artifacts.web.build", "Build locally")
	require.NoError(t, err)

	_, err = svc.GetInsights(ctx, step.ID)
	require.ErrorIs(t, err, ErrNotFound)

	start := time.Date(2026, 10, 27, 9, 55, 25, 0, time.UTC)
	report := build.Insights{
		Outcome: build.InsightsFailed, Engine: build.EngineDockerBuildx, Platform: "linux/arm64",
		StartedAt: start, FinishedAt: start.Add(22 * time.Second),
		Error:    "RUN echo hunter2-secret failed",
		Steps:    []build.InsightsStep{{Name: "RUN echo hunter2-secret", Status: build.StepError}},
		Log:      strings.Repeat("#3 2.825 warning\n", 2000) + "printed hunter2-secret",
		LogLines: 2001, LogTruncated: true,
	}
	redactor := redact.New(map[string]string{"hunter2-secret": "API_KEY"})
	require.NoError(t, svc.PutInsights(ctx, step.ID, redactor, report))

	got, err := svc.GetInsights(ctx, step.ID)
	require.NoError(t, err)
	require.Equal(t, build.InsightsFailed, got.Outcome)
	require.Equal(t, "linux/arm64", got.Platform)
	require.True(t, got.StartedAt.Equal(report.StartedAt))
	require.True(t, got.FinishedAt.Equal(report.FinishedAt))
	require.Equal(t, "RUN echo [redacted:API_KEY] failed", got.Error)
	require.Equal(t, []build.InsightsStep{{Name: "RUN echo [redacted:API_KEY]", Status: build.StepError}}, got.Steps)
	require.Equal(t, strings.Repeat("#3 2.825 warning\n", 2000)+"printed [redacted:API_KEY]", got.Log)
	require.Equal(t, 2001, got.LogLines)
	require.True(t, got.LogTruncated)

	// The log is stored compressed.
	row, err := st.GetBuildInsights(ctx, step.ID)
	require.NoError(t, err)
	require.Less(t, len(row.LogGzip), len(report.Log)/10)

	// A repeated upload replaces the report.
	report.Outcome, report.Error, report.Log = build.InsightsSucceeded, "", "done"
	require.NoError(t, svc.PutInsights(ctx, step.ID, redactor, report))
	got, err = svc.GetInsights(ctx, step.ID)
	require.NoError(t, err)
	require.Equal(t, build.InsightsSucceeded, got.Outcome)
	require.Equal(t, "done", got.Log)

	// A malformed report is refused.
	report.Outcome = "cancelled"
	require.ErrorIs(t, svc.PutInsights(ctx, step.ID, redactor, report), ErrInvalidInsights)

	// The report leaves with its run.
	_, err = pool.Exec(ctx, "DELETE FROM runs WHERE id = $1", run.ID)
	require.NoError(t, err)
	_, err = svc.GetInsights(ctx, step.ID)
	require.ErrorIs(t, err, ErrNotFound)
}
