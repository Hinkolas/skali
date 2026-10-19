// Task rendering: sequential, titled units of work outside a run tree
// (platform bootstrap, local builds). On a styled terminal the active task
// animates a spinner with a transient log tail underneath and settles into
// one glyph line; elsewhere only the settled line is printed, so scripts
// and CI logs keep one plain line per task.
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

	mu sync.Mutex
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

	note     string   // last output line, shown under the spinner
	tail     []string // recent output lines, replayed on failure
	frame    int
	live     int // lines currently on screen
	finished bool
	stop     chan struct{}
	stopped  chan struct{}
}

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
	width := TerminalWidth(task.tasks.Out)
	line := "  " + style.Cyan(style.spinner(task.frame)) + " " + Truncate(task.title, width-6)
	if elapsed := task.elapsed(); elapsed != "" {
		line += "  " + style.Dim(elapsed)
	}
	block.WriteString(line + "\n")
	task.live = 1
	if task.note != "" {
		block.WriteString("    " + style.Dim(Truncate(task.note, width-6)) + "\n")
		task.live = 2
	}
	return block.String()
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
	if replayTail {
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
