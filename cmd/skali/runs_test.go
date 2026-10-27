package main

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/build"
	"github.com/Hinkolas/skali/internal/client"
)

func TestRunCancelConfirmsWithSummary(t *testing.T) {
	install := seedAccessScope(t)
	environmentID := "p1-e1"
	install.runs["run-1"] = client.Run{ID: "run-1", Kind: "deployment", Status: "running", EnvironmentID: &environmentID}
	install.runs["run-2"] = client.Run{ID: "run-2", Kind: "backup", Status: "succeeded"}

	// The summary names the run and where it runs; declining cancels nothing.
	out, err := runCommand(t, newRunCommand(), "n\n", "cancel", "run-1")
	require.ErrorContains(t, err, "aborted")
	require.Contains(t, out, "run          run-1 (deployment)")
	require.Contains(t, out, "status       running")
	require.Contains(t, out, "environment  production")
	require.Contains(t, out, "returns the target to the prior active revision")
	require.Contains(t, out, "Cancel this deployment run? [y/N]")
	require.Empty(t, install.posts)

	// Confirming cancels and reports the fallback; a finished run is refused
	// before any question.
	out, err = runCommand(t, newRunCommand(), "y\n", "cancel", "run-1")
	require.NoError(t, err)
	require.Contains(t, out, "run run-1 cancelled")
	require.Contains(t, out, "the target returned to the prior active revision")
	require.Equal(t, []string{"cancel:run-1"}, install.posts)
	_, err = runCommand(t, newRunCommand(), "", "cancel", "run-2")
	require.ErrorContains(t, err, "run run-2 is already succeeded")

	// Closed stdin needs --yes; --yes asks nothing.
	_, err = runCommand(t, newRunCommand(), "", "cancel", "run-1")
	require.ErrorContains(t, err, "non-interactive use requires --yes")
	out, err = runCommand(t, newRunCommand(), "", "cancel", "run-1", "--yes")
	require.NoError(t, err)
	require.NotContains(t, out, "[y/N]")
}

func TestRunInsightsSummarizesEachBuild(t *testing.T) {
	install := seedAccessScope(t)
	environmentID := "p1-e1"
	install.runs["run-1"] = client.Run{ID: "run-1", Kind: "deployment", Status: "succeeded", EnvironmentID: &environmentID}
	install.steps["run-1"] = []client.Step{{ID: "artifacts", Key: "artifacts", Title: "Prepare artifacts",
		Children: []client.Step{
			{ID: "web", Key: "artifacts.web", Title: "web", Children: []client.Step{
				{ID: "web-build", Key: "artifacts.web.build", Title: "Build locally (linux/arm64)"},
				{ID: "web-upload", Key: "artifacts.web.upload", Title: "Upload to cr.example.com"},
			}},
			{ID: "worker", Key: "artifacts.worker", Title: "worker", Children: []client.Step{
				{ID: "worker-build", Key: "artifacts.worker.build", Title: "Build locally (linux/arm64)"},
			}},
		}}}
	start := time.Date(2026, 10, 27, 9, 55, 25, 0, time.UTC)
	install.insights["web-build"] = build.Insights{
		Outcome: build.InsightsSucceeded, Engine: build.EngineDockerBuildx, Platform: "linux/arm64",
		StartedAt: start, FinishedAt: start.Add(22100 * time.Millisecond),
		Steps: []build.InsightsStep{
			{Name: "[builder 1/3] COPY package.json .", Status: build.StepCached},
			{Name: "[builder 2/3] RUN bun install", Status: build.StepDone, Seconds: 2.4},
			{Name: "[builder 3/3] RUN bun run build", Status: build.StepDone, Seconds: 17.6},
		},
		Log: "#1 building\n#3 DONE 17.6s", LogLines: 2741, LogTruncated: true,
	}

	out, err := runCommand(t, newRunCommand(), "", "insights", "run-1")
	require.NoError(t, err)
	require.Contains(t, out, "web  Build locally (linux/arm64)")
	require.Contains(t, out, "outcome      succeeded")
	require.Contains(t, out, "duration     22.1s")
	require.Contains(t, out, "steps        3 steps (1 cached)")
	require.Contains(t, out, "log          2741 lines (kept its start and end)")
	slowest := out[strings.Index(out, "slowest steps"):]
	require.Less(t, strings.Index(slowest, "RUN bun run build"), strings.Index(slowest, "RUN bun install"),
		"the slowest step leads")
	require.NotContains(t, out, "#1 building", "the log needs --log")
	require.Contains(t, out, "worker  Build locally (linux/arm64)\nNo insights available for this build.")

	out, err = runCommand(t, newRunCommand(), "", "insights", "run-1", "--step", "artifacts.web.build", "--log")
	require.NoError(t, err)
	require.NotContains(t, out, "worker")
	require.Contains(t, out, "#1 building\n#3 DONE 17.6s")

	// The build step's journal points at its insights.
	out, err = runCommand(t, newRunCommand(), "", "logs", "run-1", "--step", "artifacts.web.build")
	require.NoError(t, err)
	require.Contains(t, out, "skali run insights run-1 --step artifacts.web.build --log")
}
