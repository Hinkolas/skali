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

	"github.com/charmbracelet/x/ansi"

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

// Renderer writes run trees. On a TTY the tree is a live block: every
// Render and Tick rewrites only the rows that changed, each frame is one
// write bracketed by synchronized-output markers, and the block never grows
// past the window (finished steps fold their children first, then the top
// is cut), so the cursor can always return to its first row and the
// scrollback keeps no stale copies. Finish prints the complete tree once.
// Without a TTY every snapshot is appended line by line.
type Renderer struct {
	Out   io.Writer
	TTY   bool
	Style *Style
	// Logs supplies the tail lines shown under running, waiting, and
	// failed steps, keyed by step id. It runs on every frame, so it must
	// answer from memory; see TailStepIDs for what to prefetch.
	Logs func(stepID string) []string
	// Size reports the terminal's columns and rows; nil queries Out. A
	// zero height disables the cap.
	Size func() (width, height int)
	// Footer is an extra last row of the live block on a TTY, for key hints
	// and transient notices; it is styled by the caller and never part of
	// the finished tree.
	Footer string

	frame    int
	previous []string // rows of the block currently on screen
	width    int
	height   int
	lastTree *client.RunTree
}

// treeView carries the per-render display parameters through the pure
// line builders.
type treeView struct {
	style *Style
	frame int
	now   time.Time // zero suppresses live elapsed times
	width int       // zero suppresses tail truncation
	// fold hides the children of succeeded steps, the first thing a
	// too-tall live block gives up.
	fold bool
}

// showsTail reports whether a step's log tail is displayed: live or failed
// leaves only, the children of composite steps speak for themselves.
func showsTail(step *client.Step) bool {
	return (step.Status == "running" || step.Status == "waiting" || step.Status == "failed") &&
		len(step.Children) == 0
}

// TailStepIDs lists the steps whose log tail a render would show, so the
// caller can fetch those tails ahead of the frame.
func TailStepIDs(tree *client.RunTree) []string {
	var ids []string
	var walk func(steps []client.Step)
	walk = func(steps []client.Step) {
		for index := range steps {
			if showsTail(&steps[index]) {
				ids = append(ids, steps[index].ID)
			}
			walk(steps[index].Children)
		}
	}
	walk(tree.Steps)
	return ids
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
	if logs != nil && showsTail(step) {
		for _, entry := range logs(step.ID) {
			// Structured checkpoint messages contain several lines. Split
			// before truncating and counting terminal rows for repaint.
			for _, text := range strings.Split(entry, "\n") {
				if room := view.width - len(tailIndent) - 1; view.width > 0 && room > 1 {
					text = ansi.Truncate(text, room, "…")
				}
				// Compact rows arrive styled; plain detail rows are dimmed.
				if !strings.Contains(text, "\x1b[") {
					text = view.style.Dim(text)
				}
				lines = append(lines, tailIndent+text)
			}
		}
	}
	if view.fold && step.Status == "succeeded" {
		return lines
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
	r.paint(false)
}

// Finish writes the final snapshot in full: the live block is replaced by
// the complete tree, however tall, and later output appends below it.
func (r *Renderer) Finish(tree *client.RunTree) {
	r.lastTree = tree
	r.paint(true)
	r.previous = nil
}

// Tick advances the spinner and repaints the last snapshot; between two
// server polls this keeps the display visibly alive.
func (r *Renderer) Tick() {
	if r.lastTree == nil || !r.TTY {
		return
	}
	r.frame++
	r.paint(false)
}

func (r *Renderer) paint(final bool) {
	width, height := r.size()
	view := &treeView{style: r.Style, frame: r.frame}
	if r.Style.on() {
		view.now = time.Now()
	}
	if r.TTY {
		view.width = width
	}
	lines := treeLines(r.lastTree, r.Logs, view)
	if !r.TTY {
		for _, line := range lines {
			fmt.Fprintln(r.Out, line)
		}
		return
	}
	if !final && r.Footer != "" {
		lines = append(lines, r.Footer)
	}
	fitWidth(lines, width)
	if !final && height > 1 && len(lines) > height-1 {
		view.fold = true
		lines = treeLines(r.lastTree, r.Logs, view)
		if r.Footer != "" {
			lines = append(lines, r.Footer)
		}
		fitWidth(lines, width)
		lines = cutTop(lines, height-1, r.Style)
	}

	var frame strings.Builder
	frame.WriteString("\x1b[?2026h")
	if len(r.previous) > 0 {
		fmt.Fprintf(&frame, "\x1b[%dA", len(r.previous))
		if width != r.width || height != r.height {
			// A resize invalidates the row bookkeeping; repaint everything.
			frame.WriteString("\x1b[J")
			r.previous = nil
		}
	}
	for index, line := range lines {
		if index < len(r.previous) && r.previous[index] == line {
			frame.WriteString("\n")
			continue
		}
		frame.WriteString("\r\x1b[2K")
		frame.WriteString(line)
		frame.WriteString("\n")
	}
	if len(lines) < len(r.previous) {
		frame.WriteString("\x1b[J")
	}
	frame.WriteString("\x1b[?2026l")
	_, _ = io.WriteString(r.Out, frame.String())
	r.previous = lines
	r.width, r.height = width, height
}

func (r *Renderer) size() (int, int) {
	if r.Size != nil {
		return r.Size()
	}
	return TerminalSize(r.Out)
}

// fitWidth keeps every line on one row: a wrapped line would break the
// row count the cursor movement relies on.
func fitWidth(lines []string, width int) {
	if width <= 1 {
		return
	}
	for index, line := range lines {
		lines[index] = ansi.Truncate(line, width-1, "…")
	}
}

// cutTop keeps the newest rows of a block that still exceeds the window
// after folding, replacing the hidden top with a count.
func cutTop(lines []string, rows int, style *Style) []string {
	if rows <= 0 || len(lines) <= rows {
		return lines
	}
	if rows == 1 {
		return lines[len(lines)-1:]
	}
	hidden := len(lines) - (rows - 1)
	marker := fmt.Sprintf("… %d earlier rows", hidden)
	if style.on() {
		marker = style.Dim(marker)
	}
	return append([]string{marker}, lines[hidden:]...)
}

// Detach stops rewriting: whatever is on screen stays.
func (r *Renderer) Detach() { r.previous = nil }

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
