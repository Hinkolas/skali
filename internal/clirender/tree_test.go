package clirender

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/client"
)

func timeAt(second int) *time.Time {
	t := time.Date(2026, 7, 20, 12, 0, second, 0, time.UTC)
	return &t
}

// The transcript shape: glyph columns, nesting by three levels of two
// spaces per depth step, durations right-aligned, log tails under failed
// leaves.
func TestLinesTranscriptShape(t *testing.T) {
	t.Parallel()
	tree := &client.RunTree{
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
	logs := func(stepID string) []string {
		if stepID == "s5" {
			return []string{"connection refused"}
		}
		return nil
	}

	lines := Lines(tree, logs)
	require.Equal(t, []string{
		"run 0001J9V2  deployment",
		"  ok    Validate project definition",
		"  run   Prepare artifacts",
		"        run   web",
		"              ok    Build locally (linux/arm64)           41s",
		"              fail  Push localhost:5510/skali/demo/web",
		"                      connection refused",
		"  ..    Create revision",
	}, lines)
}

func TestRendererRewritesOnTTY(t *testing.T) {
	t.Parallel()
	var out sink
	renderer := &Renderer{Out: &out, TTY: true}
	tree := &client.RunTree{Run: client.Run{ID: "0", Kind: "deployment"},
		Steps: []client.Step{{ID: "s", Title: "Step", Status: "running"}}}
	renderer.Render(tree)
	first := out.String()
	require.NotContains(t, first, "\x1b[")

	tree.Steps[0].Status = "succeeded"
	renderer.Render(tree)
	require.Contains(t, out.String()[len(first):], "\x1b[2A\x1b[J", "the second render replaces the block")
}

type sink struct{ data []byte }

func (s *sink) Write(p []byte) (int, error) { s.data = append(s.data, p...); return len(p), nil }
func (s *sink) String() string              { return string(s.data) }
