package clirender

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/client"
)

func timeAt(second int) *time.Time {
	t := time.Date(2026, 7, 20, 12, 0, second, 0, time.UTC)
	return &t
}

func exampleTree() *client.RunTree {
	return &client.RunTree{
		Run: client.Run{ID: "019f7f78-0000-7000-8000-00000001j9v2", Kind: "deployment", Status: "running"},
		Steps: []client.Step{
			{ID: "s1", Key: "validate", Title: "Validate project definition", Status: "succeeded",
				StartedAt: timeAt(0), FinishedAt: timeAt(0)},
			{ID: "s2", Key: "artifacts", Title: "Prepare artifacts", Status: "running",
				StartedAt: timeAt(1),
				Children: []client.Step{
					{ID: "s3", Key: "artifacts.web", Title: "web", Status: "running",
						Children: []client.Step{
							{ID: "s4", Key: "artifacts.web.build", Title: "Build locally (linux/arm64)",
								Status: "succeeded", StartedAt: timeAt(2), FinishedAt: timeAt(43)},
							{ID: "s5", Key: "artifacts.web.push", Title: "Push localhost:5510/skali/demo/web",
								Status: "failed"},
						}},
				}},
			{ID: "s6", Key: "revision", Title: "Create revision", Status: "pending"},
		},
	}
}

func exampleLogs(stepID string) []string {
	if stepID == "s5" {
		return []string{"connection refused"}
	}
	return nil
}

// The transcript shape: glyph columns, nesting by one two-space level per
// depth step, durations right-aligned, log tails under failed leaves.
func TestLinesTranscriptShape(t *testing.T) {
	t.Parallel()
	lines := Lines(exampleTree(), exampleLogs)
	require.Equal(t, []string{
		"run 0001J9V2  deployment",
		"  ok    Validate project definition",
		"  run   Prepare artifacts",
		"    run   web",
		"      ok    Build locally (linux/arm64)                   41s",
		"      fail  Push localhost:5510/skali/demo/web",
		"              connection refused",
		"  ..    Create revision",
	}, lines)
}

// Styled lines carry colored symbol glyphs instead of the word column and
// never leak ANSI noise into the titles themselves.
func TestStyledLines(t *testing.T) {
	t.Parallel()
	view := &treeView{style: &Style{Enabled: true}, frame: 0}
	lines := treeLines(exampleTree(), exampleLogs, view)
	joined := strings.Join(lines, "\n")
	require.Contains(t, joined, "✓")
	require.Contains(t, joined, "✗")
	require.Contains(t, joined, spinnerFrames[0])
	require.Contains(t, joined, "\x1b[32m", "succeeded renders green")
	require.Contains(t, joined, "\x1b[31m", "failed renders red")
	require.Contains(t, joined, "connection refused")
	require.NotContains(t, joined, "  ok  ", "no plain glyph column in styled mode")
}

// Waiting steps show their log tail: that is where rollout health
// summaries surface while the server verifies replicas.
func TestWaitingStepShowsTail(t *testing.T) {
	t.Parallel()
	tree := &client.RunTree{
		Run: client.Run{ID: "1", Kind: "deployment", Status: "running"},
		Steps: []client.Step{
			{ID: "v", Key: "verify", Title: "Verify health", Status: "waiting"},
		},
	}
	lines := Lines(tree, func(string) []string {
		return []string{"web: progressing (0/2 ready)"}
	})
	require.Contains(t, lines, "          web: progressing (0/2 ready)")
}

func TestRendererRewritesOnTTY(t *testing.T) {
	t.Parallel()
	var out sink
	renderer := &Renderer{Out: &out, TTY: true, Size: func() (int, int) { return 80, 0 }}
	tree := &client.RunTree{Run: client.Run{ID: "0", Kind: "deployment"},
		Steps: []client.Step{{ID: "s", Title: "Step", Status: "running"}}}
	renderer.Render(tree)
	first := out.String()
	require.NotContains(t, first, "\x1b[2A", "the first frame has nothing to move over")
	require.Contains(t, first, "run 0  deployment")
	require.Contains(t, first, "run   Step")

	tree.Steps[0].Status = "succeeded"
	renderer.Render(tree)
	second := out.String()[len(first):]
	require.Contains(t, second, "\x1b[2A", "the second frame returns to the top of the block")
	require.NotContains(t, second, "run 0  deployment", "an unchanged row is skipped, not rewritten")
	require.Contains(t, second, "\r\x1b[2K  ok    Step\n", "the changed row is cleared and rewritten")
	require.True(t, strings.HasPrefix(second, "\x1b[?2026h"), "frames are bracketed for synchronized output")
	require.True(t, strings.HasSuffix(second, "\x1b[?2026l"))

	// Fewer rows than before clear what the old block left below.
	tree.Steps = nil
	renderer.Render(tree)
	third := out.String()[len(first)+len(second):]
	require.Contains(t, third, "\x1b[J")
}

func TestRendererFoldsThenCutsToWindowHeight(t *testing.T) {
	t.Parallel()
	tree := &client.RunTree{Run: client.Run{ID: "0", Kind: "deployment"}, Steps: []client.Step{
		{ID: "prep", Title: "Prepare artifacts", Status: "succeeded", Children: []client.Step{
			{ID: "b1", Title: "Build one", Status: "succeeded"},
			{ID: "b2", Title: "Build two", Status: "succeeded"},
		}},
		{ID: "roll", Title: "Roll out revision", Status: "running", Children: []client.Step{
			{ID: "apply", Title: "Apply web", Status: "running"},
			{ID: "tls", Title: "Issue TLS certificate", Status: "waiting"},
		}},
	}}
	logs := func(id string) []string {
		if id == "tls" {
			return []string{"phase: pending", "order: pending"}
		}
		return nil
	}

	// Nine rows in full; a window of ten rows shows all of them.
	full := &Renderer{Out: &sink{}, TTY: true, Logs: logs, Size: func() (int, int) { return 80, 10 }}
	full.Render(tree)
	require.Len(t, full.previous, 9)
	require.Contains(t, strings.Join(full.previous, "\n"), "Build one")

	// Eight rows: folding the succeeded step's children fits exactly.
	folded := &Renderer{Out: &sink{}, TTY: true, Logs: logs, Size: func() (int, int) { return 80, 8 }}
	folded.Render(tree)
	require.Len(t, folded.previous, 7)
	require.NotContains(t, strings.Join(folded.previous, "\n"), "Build one")
	require.Contains(t, strings.Join(folded.previous, "\n"), "order: pending")

	// Five rows: the top is cut and the cut is announced.
	cut := &Renderer{Out: &sink{}, TTY: true, Logs: logs, Size: func() (int, int) { return 80, 5 }}
	cut.Render(tree)
	require.Len(t, cut.previous, 4)
	require.Equal(t, "… 4 earlier rows", cut.previous[0])
	require.Contains(t, cut.previous[3], "order: pending")

	// Finish prints everything and leaves nothing to rewrite.
	cut.Finish(tree)
	require.Nil(t, cut.previous)
	require.Contains(t, cut.Out.(*sink).String(), "Build one")
}

func TestRendererKeepsRowsWithinWidth(t *testing.T) {
	t.Parallel()
	tree := &client.RunTree{Run: client.Run{ID: "0", Kind: "deployment"},
		Steps: []client.Step{{ID: "s", Title: strings.Repeat("long title ", 10), Status: "running"}}}
	renderer := &Renderer{Out: &sink{}, TTY: true, Size: func() (int, int) { return 40, 0 }}
	renderer.Render(tree)
	for _, row := range renderer.previous {
		require.LessOrEqual(t, len([]rune(row)), 39)
	}
}

func TestTailStepIDs(t *testing.T) {
	t.Parallel()
	tree := &client.RunTree{Steps: []client.Step{
		{ID: "done", Status: "succeeded"},
		{ID: "parent", Status: "running", Children: []client.Step{{ID: "leaf", Status: "waiting"}}},
		{ID: "failed", Status: "failed"},
	}}
	require.Equal(t, []string{"leaf", "failed"}, TailStepIDs(tree))
}

type sink struct{ data []byte }

func (s *sink) Write(p []byte) (int, error) { s.data = append(s.data, p...); return len(p), nil }
func (s *sink) String() string              { return string(s.data) }

func TestMultilineCheckpointRows(t *testing.T) {
	tree := &client.RunTree{Run: client.Run{ID: "1", Kind: "deployment"}, Steps: []client.Step{{ID: "tls", Title: "Issue TLS certificate", Status: "waiting"}}}
	lines := Lines(tree, func(string) []string {
		return []string{"Attempt 2\nNext retry: 2026-09-10T23:00:00Z\nACME order invalid"}
	})
	require.Len(t, lines, 5)
	require.Equal(t, "          Next retry: 2026-09-10T23:00:00Z", lines[3])
	for _, line := range lines {
		require.NotContains(t, line, "\n", "each counted row must be one physical terminal line")
	}
}
