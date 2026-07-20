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
		task.paint()
		t.mu.Unlock()
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
			task.paint()
			task.tasks.mu.Unlock()
		}
	}
}

// paint redraws the live block; the caller holds the printer lock.
func (task *Task) paint() {
	task.erase()
	style := task.tasks.Style
	width := terminalWidth(task.tasks.Out)
	line := "  " + style.Cyan(style.spinner(task.frame)) + " " + truncate(task.title, width-6)
	if elapsed := task.elapsed(); elapsed != "" {
		line += "  " + style.Dim(elapsed)
	}
	fmt.Fprintln(task.tasks.Out, line)
	task.live = 1
	if task.note != "" {
		fmt.Fprintln(task.tasks.Out, "    "+style.Dim(truncate(task.note, width-6)))
		task.live = 2
	}
}

func (task *Task) erase() {
	if task.live > 0 {
		fmt.Fprintf(task.tasks.Out, "\x1b[%dA\x1b[J", task.live)
		task.live = 0
	}
}

// Note publishes a progress line: it becomes the transient tail under the
// spinner and enters the failure replay buffer.
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
	if task.stop != nil && !task.finished {
		task.paint()
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
	task.erase()

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
