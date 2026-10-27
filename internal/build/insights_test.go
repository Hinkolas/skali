package build

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func fixedClock(times ...time.Time) func() time.Time {
	var mu sync.Mutex
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		now := times[0]
		if len(times) > 1 {
			times = times[1:]
		}
		return now
	}
}

// The recorder follows BuildKit's plain progress: a vertex's first line
// names it, and its verdict line says how it ended.
func TestInsightsRecorderFollowsBuildKitSteps(t *testing.T) {
	start := time.Date(2026, 10, 27, 9, 55, 25, 0, time.UTC)
	recorder := newInsightsRecorder(EngineDockerBuildx, "linux/arm64", fixedClock(start, start.Add(22*time.Second)))
	for _, line := range []string{
		`#0 building with "desktop-linux" instance using docker driver`,
		"#1 [internal] load build definition from Dockerfile",
		"#1 transferring dockerfile: 5.21kB done",
		"#1 DONE 0.0s",
		"#2 [builder 1/3] COPY package.json .",
		"#2 CACHED",
		"#3 [builder 2/3] RUN bun run build",
		"#3 2.825 \x1b[33mwarning\x1b[0m: script_context_deprecated",
		"#4 [builder 3/3] RUN false",
		"#3 DONE 17.6s",
		"#4 ERROR: process did not complete successfully: exit code: 1",
		"#5 exporting to image",
		"#5 CANCELED",
		"#6 [stage-1 2/2] COPY --from=builder /app .",
	} {
		recorder.Line("info", line)
	}
	report := recorder.Finish(errors.New("build: buildx build failed: exit status 1"))
	require.NoError(t, report.Validate())
	require.Equal(t, InsightsFailed, report.Outcome)
	require.Equal(t, "build: buildx build failed: exit status 1", report.Error)
	require.Equal(t, EngineDockerBuildx, report.Engine)
	require.Equal(t, "linux/arm64", report.Platform)
	require.Equal(t, 22*time.Second, report.FinishedAt.Sub(report.StartedAt))
	require.Equal(t, []InsightsStep{
		{Name: "[internal] load build definition from Dockerfile", Status: StepDone},
		{Name: "[builder 1/3] COPY package.json .", Status: StepCached},
		{Name: "[builder 2/3] RUN bun run build", Status: StepDone, Seconds: 17.6},
		{Name: "[builder 3/3] RUN false", Status: StepError},
		{Name: "exporting to image", Status: StepCanceled},
		{Name: "[stage-1 2/2] COPY --from=builder /app .", Status: StepIncomplete},
	}, report.Steps)
	require.Equal(t, 14, report.LogLines)
	require.False(t, report.LogTruncated)
	require.Contains(t, report.Log, "#3 2.825 warning: script_context_deprecated", "colors are stripped")
	require.True(t, strings.HasPrefix(report.Log, `#0 building with "desktop-linux"`))
}

// A retried builder invocation (the daemon-export fallback) numbers its
// vertices afresh; the report describes the build that produced the image.
func TestInsightsRecorderKeepsTheLastInvocation(t *testing.T) {
	recorder := NewInsightsRecorder(EngineDockerBuildx, "linux/amd64")
	for _, line := range []string{
		`#0 building with "default" instance using docker driver`,
		"#1 [internal] load build definition from Dockerfile",
		"#1 DONE 0.1s",
		"#2 exporting to oci image format",
		"#2 ERROR: OCI exporter is not supported for the docker driver",
		"builder does not support the OCI exporter; exporting through the docker image store instead",
		`#0 building with "default" instance using docker driver`,
		"#1 [internal] load build definition from Dockerfile",
		"#1 DONE 0.0s",
		"#2 exporting to image",
		"#2 DONE 1.2s",
	} {
		recorder.Line("info", line)
	}
	report := recorder.Finish(nil)
	require.Equal(t, InsightsSucceeded, report.Outcome)
	require.Empty(t, report.Error)
	require.Equal(t, []InsightsStep{
		{Name: "[internal] load build definition from Dockerfile", Status: StepDone},
		{Name: "exporting to image", Status: StepDone, Seconds: 1.2},
	}, report.Steps)
	require.Equal(t, 11, report.LogLines, "the log keeps both invocations")
}

// A log over the bound keeps its head and its end, where a failure
// explains itself, around one omission line.
func TestInsightsRecorderBoundsTheLog(t *testing.T) {
	recorder := NewInsightsRecorder(EngineDockerBuildx, "linux/amd64")
	line := strings.Repeat("x", 1000)
	const total = 4000
	for index := range total {
		recorder.Line("info", fmt.Sprintf("%05d %s", index, line))
	}
	recorder.Line("info", strings.Repeat("y", 3*MaxInsightsLineBytes))
	report := recorder.Finish(nil)
	require.NoError(t, report.Validate())
	require.True(t, report.LogTruncated)
	require.Equal(t, total+1, report.LogLines)
	require.LessOrEqual(t, len(report.Log), MaxInsightsLogBytes+maxInsightsMarkerBytes)
	lines := strings.Split(report.Log, "\n")
	require.True(t, strings.HasPrefix(lines[0], "00000 "), "the head stays")
	require.Contains(t, report.Log, "lines omitted …")
	require.True(t, strings.HasPrefix(lines[len(lines)-2], fmt.Sprintf("%05d ", total-1)), "the end stays")
	last := lines[len(lines)-1]
	require.True(t, strings.HasSuffix(last, "… [line truncated]"))
	require.LessOrEqual(t, len(last), MaxInsightsLineBytes+len(" … [line truncated]"))
}

func TestInsightsValidate(t *testing.T) {
	start := time.Date(2026, 10, 27, 9, 55, 25, 0, time.UTC)
	valid := Insights{Outcome: InsightsSucceeded, StartedAt: start, FinishedAt: start.Add(time.Second),
		Steps: []InsightsStep{{Name: "RUN make", Status: StepDone, Seconds: 1}}}
	require.NoError(t, valid.Validate())

	for name, mutate := range map[string]func(*Insights){
		"outcome":       func(i *Insights) { i.Outcome = "cancelled" },
		"missing times": func(i *Insights) { i.StartedAt = time.Time{} },
		"reversed":      func(i *Insights) { i.FinishedAt = start.Add(-time.Second) },
		"step status":   func(i *Insights) { i.Steps[0].Status = "running" },
		"step name":     func(i *Insights) { i.Steps[0].Name = "" },
		"duration":      func(i *Insights) { i.Steps[0].Seconds = -1 },
		"log":           func(i *Insights) { i.Log = strings.Repeat("x", MaxInsightsLogBytes+maxInsightsMarkerBytes+1) },
		"steps":         func(i *Insights) { i.Steps = make([]InsightsStep, MaxInsightsSteps+1) },
		"error":         func(i *Insights) { i.Error = strings.Repeat("x", maxInsightsTextBytes+1) },
	} {
		report := valid
		report.Steps = append([]InsightsStep(nil), valid.Steps...)
		mutate(&report)
		require.Error(t, report.Validate(), name)
	}
}
