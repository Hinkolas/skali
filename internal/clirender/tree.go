// Package clirender renders journal run trees in the terminal: the
// transcript-shaped glyph tree with durations and inline log tails, live
// rewritten in place on a TTY and appended line by line otherwise. On a
// styled terminal the glyphs become colored symbols with animated
// spinners; the plain line construction is pure so golden tests pin the
// shape.
package clirender

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Hinkolas/skali/internal/client"
)

// glyphs by step status, plain mode; the column keeps the tree aligned.
var glyphs = map[string]string{
	"pending":   "..",
	"waiting":   "wait",
	"running":   "run",
	"succeeded": "ok",
	"failed":    "fail",
	"skipped":   "skip",
	"cancelled": "stop",
}

// durationColumn right-aligns step durations.
const durationColumn = 58

// Renderer writes run trees; on a TTY every Render replaces the previous
// block in place, and Tick advances the spinner between snapshots.
type Renderer struct {
	Out   io.Writer
	TTY   bool
	Style *Style
	// Logs supplies the tail lines shown under running, waiting, and
	// failed steps, keyed by step id.
	Logs func(stepID string) []string

	frame         int
	previousLines int
	lastTree      *client.RunTree
}

// treeView carries the per-render display parameters through the pure
// line builders.
type treeView struct {
	style *Style
	frame int
	now   time.Time // zero suppresses live elapsed times
	width int       // zero suppresses tail truncation
}

// Lines builds the plain display block for one tree snapshot.
func Lines(tree *client.RunTree, logs func(stepID string) []string) []string {
	return treeLines(tree, logs, &treeView{})
}

func treeLines(tree *client.RunTree, logs func(stepID string) []string, view *treeView) []string {
	header := fmt.Sprintf("run %s  %s", shortRunID(tree.Run.ID), tree.Run.Kind)
	if view.style.on() {
		header = view.style.Dim("run ") + view.style.Bold(shortRunID(tree.Run.ID)) +
			"  " + tree.Run.Kind
	}
	lines := []string{header}
	for index := range tree.Steps {
		lines = append(lines, stepLines(&tree.Steps[index], 1, logs, view)...)
	}
	return lines
}

func stepLines(step *client.Step, depth int, logs func(stepID string) []string, view *treeView) []string {
	indent := strings.Repeat("  ", depth)
	var line string
	var tailIndent string
	if view.style.on() {
		line, tailIndent = styledStepLine(step, indent, view)
	} else {
		glyph := glyphs[step.Status]
		if glyph == "" {
			glyph = step.Status
		}
		line = fmt.Sprintf("%s%-4s  %s", indent, glyph, step.Title)
		if duration := stepDuration(step, time.Time{}); duration != "" {
			line = pad(line, durationColumn) + duration
		}
		tailIndent = indent + "        "
	}
	lines := []string{line}
	showTail := step.Status == "running" || step.Status == "waiting" || step.Status == "failed"
	if logs != nil && showTail && len(step.Children) == 0 {
		for _, entry := range logs(step.ID) {
			// Structured checkpoint messages contain several lines. Split
			// before truncating and counting terminal rows for repaint.
			for _, text := range strings.Split(entry, "\n") {
				if view.width > 0 {
					text = Truncate(text, view.width-len(tailIndent)-1)
				}
				lines = append(lines, tailIndent+view.style.Dim(text))
			}
		}
	}
	for index := range step.Children {
		lines = append(lines, stepLines(&step.Children[index], depth+1, logs, view)...)
	}
	return lines
}

// styledStepLine builds one colored step line; the pad math runs on
// visible cells because the ANSI codes have no width.
func styledStepLine(step *client.Step, indent string, view *treeView) (line, tailIndent string) {
	style := view.style
	title := step.Title
	switch step.Status {
	case "failed":
		title = style.Red(title)
	case "pending", "skipped", "cancelled":
		title = style.Dim(title)
	}
	visible := len(indent) + 2 + len([]rune(step.Title))
	line = indent + style.Glyph(step.Status, view.frame) + " " + title
	if duration := stepDuration(step, view.now); duration != "" {
		if visible < durationColumn {
			line += spaces(durationColumn - visible)
		} else {
			line += "  "
		}
		line += style.Dim(duration)
	}
	return line, indent + "    "
}

// Render writes the current snapshot, replacing the previous one on a TTY.
func (r *Renderer) Render(tree *client.RunTree) {
	r.lastTree = tree
	r.paint()
}

// Tick advances the spinner and repaints the last snapshot; between two
// server polls this keeps the display visibly alive.
func (r *Renderer) Tick() {
	if r.lastTree == nil || !r.TTY {
		return
	}
	r.frame++
	r.paint()
}

func (r *Renderer) paint() {
	view := &treeView{style: r.Style, frame: r.frame}
	if r.Style.on() {
		view.now = time.Now()
		view.width = TerminalWidth(r.Out)
	}
	lines := treeLines(r.lastTree, r.Logs, view)
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

// stepDuration is the finished step's runtime; with a non-zero now,
// running and waiting steps report their live elapsed time instead.
func stepDuration(step *client.Step, now time.Time) string {
	if step.StartedAt == nil {
		return ""
	}
	end := step.FinishedAt
	if end == nil {
		if now.IsZero() || (step.Status != "running" && step.Status != "waiting") {
			return ""
		}
		end = &now
	}
	elapsed := end.Sub(*step.StartedAt).Round(time.Second)
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
