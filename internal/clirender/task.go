// Task rendering: sequential, titled units of work outside a run tree
// (platform bootstrap, local builds, image uploads). On a styled terminal
// the active task animates a spinner with a transient log tail or a
// progress bar underneath and settles into one glyph line; elsewhere only
// the settled line is printed, so scripts and CI logs keep one plain line
// per task, unless the task narrates its already rate-limited progress.
package clirender

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Tasks prints tasks to one writer; at most one task is live at a time.
type Tasks struct {
	Out   io.Writer
	Style *Style

	mu    sync.Mutex
	width int // overrides the terminal width; tests only
}

func (t *Tasks) columns() int {
	if t.width > 0 {
		return t.width
	}
	return TerminalWidth(t.Out)
}

func NewTasks(out io.Writer) *Tasks {
	return &Tasks{Out: out, Style: StyleFor(out)}
}

// tailLines is how much recent task output a failure replays.
const tailLines = 12

// Task is one unit of work between Start and Done, Skip, or Fail.
type Task struct {
	tasks   *Tasks
	title   string
	started time.Time

	note     string        // last output line, shown under the spinner
	progress *TaskProgress // replaces the note when set
	tail     []string      // recent output lines, replayed on failure
	narrated bool          // plain output prints notes as they arrive
	frame    int
	live     int // lines currently on screen
	finished bool
	stop     chan struct{}
	stopped  chan struct{}
}

// TaskProgress is a task's live progress display: a bar when HasFraction,
// the Status beside it, and an optional Detail line below.
type TaskProgress struct {
	Fraction    float64
	HasFraction bool
	Status      string
	Detail      string
}

// progressBarWidth is the bar's width in terminal cells; it shows only
// when progressMinStatus cells remain for the status beside it.
const (
	progressBarWidth  = 24
	progressMinStatus = 20
)

// Start begins a task. On a styled terminal it appears immediately with a
// spinner; otherwise nothing is printed until the task settles.
func (t *Tasks) Start(title string) *Task {
	task := &Task{tasks: t, title: title, started: time.Now()}
	if t.Style.on() {
		task.stop = make(chan struct{})
		task.stopped = make(chan struct{})
		t.mu.Lock()
		block := task.render()
		t.mu.Unlock()
		_, _ = io.WriteString(t.Out, block)
		go task.spin()
	}
	return task
}

func (task *Task) spin() {
	defer close(task.stopped)
	ticker := time.NewTicker(120 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-task.stop:
			return
		case <-ticker.C:
			task.tasks.mu.Lock()
			task.frame++
			block := task.render()
			task.tasks.mu.Unlock()
			// The write happens outside the lock: a slow terminal holds up
			// the animation, never the notes arriving from the work.
			_, _ = io.WriteString(task.tasks.Out, block)
		}
	}
}

// render builds the live block, erasing the one on screen, as one write.
// The caller holds the printer lock; only the spinner goroutine writes
// while the task is live, so the block lands in order.
func (task *Task) render() string {
	var block strings.Builder
	block.WriteString(task.erase())
	style := task.tasks.Style
	width := task.tasks.columns()
	line := "  " + style.Cyan(style.spinner(task.frame)) + " " + Truncate(task.title, width-6)
	if elapsed := task.elapsed(); elapsed != "" {
		line += "  " + style.Dim(elapsed)
	}
	block.WriteString(line + "\n")
	task.live = 1
	switch {
	case task.progress != nil:
		progress := task.progress
		line := "    "
		status := Truncate(progress.Status, width-6)
		// A narrow terminal drops the bar rather than wrap the line.
		if progress.HasFraction && width >= progressBarWidth+progressMinStatus+7 {
			line += progressBar(style, progress.Fraction) + " "
			status = Truncate(progress.Status, width-progressBarWidth-7)
		}
		block.WriteString(line + status + "\n")
		task.live++
		if progress.Detail != "" {
			block.WriteString("    " + style.Dim(Truncate(progress.Detail, width-6)) + "\n")
			task.live++
		}
	case task.note != "":
		block.WriteString("    " + style.Dim(Truncate(task.note, width-6)) + "\n")
		task.live = 2
	}
	return block.String()
}

// progressBar draws fraction (clamped to 0..1) as a fixed-width bar.
func progressBar(style *Style, fraction float64) string {
	filled := int(min(max(fraction, 0), 1) * progressBarWidth)
	return style.Cyan(strings.Repeat("━", filled)) + style.Muted(strings.Repeat("━", progressBarWidth-filled))
}

// erase returns the sequence that clears the live block.
func (task *Task) erase() string {
	if task.live == 0 {
		return ""
	}
	sequence := fmt.Sprintf("\x1b[%dA\x1b[J", task.live)
	task.live = 0
	return sequence
}

// Note publishes a progress line: it becomes the transient tail under the
// spinner, shown from the next frame on, and enters the failure replay
// buffer. It never writes to the terminal itself, so the work reporting
// progress never waits on the display.
//
// A narrated task on plain output is the exception: there each note is
// printed as it arrives, so it must come rate-limited and from work that
// can afford to wait on the writer.
func (task *Task) Note(line string) {
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return
	}
	task.tasks.mu.Lock()
	defer task.tasks.mu.Unlock()
	task.note = line
	task.tail = append(task.tail, line)
	if len(task.tail) > tailLines {
		task.tail = task.tail[len(task.tail)-tailLines:]
	}
	if task.narrated && !task.finished {
		fmt.Fprintln(task.tasks.Out, "        "+line)
	}
}

// Progress replaces the transient note under the spinner with a progress
// display, shown from the next frame on. Like Note it never writes to the
// terminal itself; plain output has no live display (see Narrate).
func (task *Task) Progress(progress TaskProgress) {
	task.tasks.mu.Lock()
	defer task.tasks.mu.Unlock()
	task.progress = &progress
}

// Narrate makes the task print its notes on plain output as they arrive,
// below a header line, so a long transfer leaves a readable trail in
// scripts and CI logs. Styled output is unaffected.
func (task *Task) Narrate() {
	task.tasks.mu.Lock()
	defer task.tasks.mu.Unlock()
	if task.narrated || task.finished || task.tasks.Style.on() {
		return
	}
	task.narrated = true
	fmt.Fprintf(task.tasks.Out, "  %-4s  %s\n", glyphs["running"], task.title)
	for _, entry := range task.tail {
		fmt.Fprintln(task.tasks.Out, "        "+entry)
	}
}

// NoteWriter adapts Note into an io.Writer for exec-style output streams.
func (task *Task) NoteWriter() io.Writer { return &noteWriter{task: task} }

type noteWriter struct {
	task    *Task
	partial string
}

func (w *noteWriter) Write(p []byte) (int, error) {
	w.partial += string(p)
	for {
		index := strings.IndexByte(w.partial, '\n')
		if index < 0 {
			return len(p), nil
		}
		w.task.Note(w.partial[:index])
		w.partial = w.partial[index+1:]
	}
}

// Done settles the task as succeeded; detail is appended dimmed.
func (task *Task) Done(detail string) { task.finish("succeeded", detail, false) }

// Skip settles the task as not needed; detail says why.
func (task *Task) Skip(detail string) { task.finish("skipped", detail, false) }

// Fail settles the task as failed and replays its recent output, so the
// cause survives the transient spinner display.
func (task *Task) Fail() { task.finish("failed", "", true) }

// Cancel settles the task as stopped by an interrupt. Nothing failed, so
// its output is not replayed.
func (task *Task) Cancel() { task.finish("cancelled", "", false) }

func (task *Task) finish(status, detail string, replayTail bool) {
	if task.stop != nil {
		close(task.stop)
		<-task.stopped
	}
	task.tasks.mu.Lock()
	defer task.tasks.mu.Unlock()
	if task.finished {
		return
	}
	task.finished = true
	_, _ = io.WriteString(task.tasks.Out, task.erase())

	style := task.tasks.Style
	var line string
	if style.on() {
		line = "  " + style.Glyph(status, 0) + " " + task.title
		if detail != "" {
			line += "  " + style.Dim(detail)
		}
		if elapsed := task.elapsed(); elapsed != "" {
			line += "  " + style.Dim(elapsed)
		}
	} else {
		line = fmt.Sprintf("  %-4s  %s", glyphs[status], task.title)
		if detail != "" {
			line += "  " + detail
		}
		if elapsed := task.elapsed(); elapsed != "" {
			line += "  " + elapsed
		}
	}
	fmt.Fprintln(task.tasks.Out, line)
	// A narrated task already printed its output.
	if replayTail && !task.narrated {
		for _, entry := range task.tail {
			fmt.Fprintln(task.tasks.Out, "        "+style.Dim(entry))
		}
	}
}

// elapsed reports the task's runtime once it is long enough to matter.
func (task *Task) elapsed() string {
	elapsed := time.Since(task.started).Round(time.Second)
	if elapsed < time.Second {
		return ""
	}
	return elapsed.String()
}
