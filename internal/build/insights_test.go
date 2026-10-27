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
	require.Equal(t, strings.Repeat("y", 3*MaxInsightsLineBytes), lines[len(lines)-1],
		"the recorder never cuts a line: it cannot know where a secret sits")
}

// Sealing redacts before it cuts, so a cut never leaves a fragment of a
// secret the redactor would have recognized whole.
func TestInsightsSealRedactsBeforeCutting(t *testing.T) {
	secret := "hunter2-secret"
	redact := strings.NewReplacer(secret, "[redacted:API_KEY]").Replace
	name := strings.Repeat("n", maxInsightsNameBytes-4) + secret
	line := strings.Repeat("l", MaxInsightsLineBytes-4) + secret
	start := time.Date(2026, 10, 27, 9, 55, 25, 0, time.UTC)
	report := Insights{Outcome: InsightsFailed, StartedAt: start, FinishedAt: start,
		Error: strings.Repeat("e", maxInsightsTextBytes-4) + secret,
		Steps: []InsightsStep{{Name: name, Status: StepError}},
		Log:   "#1 start\n" + line + "\n#1 ERROR",
	}
	require.NoError(t, report.Validate())
	sealed := report.Seal(redact)
	for _, text := range []string{sealed.Steps[0].Name, sealed.Error, sealed.Log} {
		require.NotContains(t, text, "hunt", "no fragment of the secret survives")
	}
	require.LessOrEqual(t, len(sealed.Steps[0].Name), maxInsightsNameBytes)
	require.LessOrEqual(t, len(sealed.Error), maxInsightsTextBytes)
	lines := strings.Split(sealed.Log, "\n")
	require.Equal(t, "#1 start", lines[0])
	require.LessOrEqual(t, len(lines[1]), MaxInsightsLineBytes)
	require.Equal(t, "#1 ERROR", lines[2])
	require.False(t, sealed.LogTruncated)
}

// Redaction may lengthen a log past its bound; sealing keeps the log's
// start and end within it and says so.
func TestInsightsSealBoundsALogRedactionLengthened(t *testing.T) {
	redact := strings.NewReplacer("x", "[redacted:SESSION_SECRET]").Replace
	var log strings.Builder
	for index := range 2000 {
		fmt.Fprintf(&log, "%05d %s\n", index, strings.Repeat("x", 1000))
	}
	log.WriteString("#9 ERROR: the end")
	start := time.Date(2026, 10, 27, 9, 55, 25, 0, time.UTC)
	report := Insights{Outcome: InsightsFailed, StartedAt: start, FinishedAt: start, Log: log.String(), LogLines: 2001}
	require.NoError(t, report.Validate())
	sealed := report.Seal(redact)
	require.True(t, sealed.LogTruncated)
	require.LessOrEqual(t, len(sealed.Log), MaxStoredInsightsLogBytes)
	require.True(t, strings.HasPrefix(sealed.Log, "00000 [redacted:SESSION_SECRET]"))
	require.True(t, strings.HasSuffix(sealed.Log, "#9 ERROR: the end"))
	require.Equal(t, 2001, sealed.LogLines)
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
		"error":         func(i *Insights) { i.Error = strings.Repeat("x", maxUploadErrorBytes+1) },
		"name length":   func(i *Insights) { i.Steps[0].Name = strings.Repeat("x", maxUploadNameBytes+1) },
	} {
		report := valid
		report.Steps = append([]InsightsStep(nil), valid.Steps...)
		mutate(&report)
		require.Error(t, report.Validate(), name)
	}
}
