// Package clirender renders journal run trees in the terminal: the
// transcript-shaped glyph tree with durations and inline log tails, live
// rewritten in place on a TTY and appended line by line otherwise. The
// line construction is pure so golden tests pin the shape.
package clirender

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Hinkolas/skali/internal/client"
)

// glyphs by step status; two columns keep the tree aligned.
var glyphs = map[string]string{
	"pending":   "..",
	"waiting":   "wait",
	"running":   "run",
	"succeeded": "ok",
	"failed":    "fail",
	"skipped":   "skip",
	"cancelled": "stop",
}

// Renderer writes run trees; on a TTY every Render replaces the previous
// block in place.
type Renderer struct {
	Out io.Writer
	TTY bool
	// Logs supplies the tail lines shown under running and failed steps,
	// keyed by step id.
	Logs func(stepID string) []string

	previousLines int
}

// Lines builds the complete display block for one tree snapshot.
func Lines(tree *client.RunTree, logs func(stepID string) []string) []string {
	header := fmt.Sprintf("run %s  %s", shortRunID(tree.Run.ID), tree.Run.Kind)
	lines := []string{header}
	for index := range tree.Steps {
		lines = append(lines, stepLines(&tree.Steps[index], 1, logs)...)
	}
	return lines
}

func stepLines(step *client.Step, depth int, logs func(stepID string) []string) []string {
	indent := strings.Repeat("  ", depth)
	glyph := glyphs[step.Status]
	if glyph == "" {
		glyph = step.Status
	}
	line := fmt.Sprintf("%s%-4s  %s", indent, glyph, step.Title)
	if duration := stepDuration(step); duration != "" {
		line = pad(line, 58) + duration
	}
	lines := []string{line}
	if logs != nil && (step.Status == "running" || step.Status == "failed") && len(step.Children) == 0 {
		for _, entry := range logs(step.ID) {
			lines = append(lines, indent+"        "+entry)
		}
	}
	for index := range step.Children {
		lines = append(lines, stepLines(&step.Children[index], depth+3, logs)...)
	}
	return lines
}

// Render writes the current snapshot, replacing the previous one on a TTY.
func (r *Renderer) Render(tree *client.RunTree) {
	lines := Lines(tree, r.Logs)
	if r.TTY && r.previousLines > 0 {
		// Move to the top of the previous block and clear to the end.
		fmt.Fprintf(r.Out, "\x1b[%dA\x1b[J", r.previousLines)
	}
	for _, line := range lines {
		fmt.Fprintln(r.Out, line)
	}
	r.previousLines = len(lines)
}

// Detach stops rewriting: whatever is on screen stays.
func (r *Renderer) Detach() { r.previousLines = 0 }

func stepDuration(step *client.Step) string {
	if step.StartedAt == nil || step.FinishedAt == nil {
		return ""
	}
	elapsed := step.FinishedAt.Sub(*step.StartedAt).Round(time.Second)
	if elapsed < time.Second {
		return ""
	}
	return elapsed.String()
}

func pad(line string, width int) string {
	if len(line) >= width {
		return line + "  "
	}
	return line + strings.Repeat(" ", width-len(line))
}

// shortRunID keeps the transcript's compact run handle: the tail of the
// UUID, uppercased, is stable and unambiguous enough for a terminal.
func shortRunID(id string) string {
	trimmed := strings.ReplaceAll(id, "-", "")
	if len(trimmed) > 8 {
		trimmed = trimmed[len(trimmed)-8:]
	}
	return strings.ToUpper(trimmed)
}
