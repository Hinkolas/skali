package clirender

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// Plain mode prints exactly one settled line per task, nothing while the
// task runs: the shape scripts and CI logs depend on.
func TestTaskPlainOutput(t *testing.T) {
	t.Parallel()
	var out sink
	tasks := &Tasks{Out: &out, Style: &Style{}}

	task := tasks.Start("Create k3d cluster skali-dev")
	require.Empty(t, out.String(), "nothing prints before the task settles")
	task.Done("state retained")

	tasks.Start("Import skalid:dev").Done("")
	tasks.Start("artifact for web").Skip("current, 4b8b0b4d722c")

	failing := tasks.Start("Bootstrap database")
	failing.Note("waiting for cluster skali-db")
	failing.Fail()

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	require.Equal(t, []string{
		"  ok    Create k3d cluster skali-dev  state retained",
		"  ok    Import skalid:dev",
		"  skip  artifact for web  current, 4b8b0b4d722c",
		"  fail  Bootstrap database",
		"        waiting for cluster skali-db",
	}, lines)
}

// Styled tasks animate on a goroutine while notes arrive from the build
// engine's stream goroutines; the printer must serialize all of it.
func TestTaskConcurrentNotes(t *testing.T) {
	t.Parallel()
	var out sink
	tasks := &Tasks{Out: &out, Style: &Style{Enabled: true}}
	task := tasks.Start("Build web (linux/arm64)")

	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			writer := task.NoteWriter()
			for range 50 {
				_, _ = writer.Write([]byte("#8 exporting layers\n"))
			}
		})
	}
	wg.Wait()
	task.Done("4b8b0b4d722c")

	require.Contains(t, out.String(), "✓")
	require.Contains(t, out.String(), "Build web (linux/arm64)")
}

// stuckTerminal accepts the first write and holds every later one until
// released, like a terminal that stopped draining its output.
type stuckTerminal struct {
	mu     sync.Mutex
	writes int
	gate   chan struct{}
}

func (w *stuckTerminal) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.writes++
	first := w.writes == 1
	w.mu.Unlock()
	if !first {
		<-w.gate
	}
	return len(p), nil
}

func (w *stuckTerminal) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writes
}

// Notes come from the work itself (a build's output stream); a slow
// terminal may hold up the spinner but never the work.
func TestTaskNoteNeverWaitsOnTheTerminal(t *testing.T) {
	t.Parallel()
	out := &stuckTerminal{gate: make(chan struct{})}
	release := sync.OnceFunc(func() { close(out.gate) })
	t.Cleanup(release)
	tasks := &Tasks{Out: out, Style: &Style{Enabled: true}}
	task := tasks.Start("Build web (linux/arm64)")
	require.Eventually(t, func() bool { return out.count() >= 2 },
		5*time.Second, 5*time.Millisecond, "the spinner is stuck in a write")

	noted := make(chan struct{})
	go func() {
		task.Note("#8 exporting layers")
		close(noted)
	}()
	select {
	case <-noted:
	case <-time.After(time.Second):
		t.Fatal("Note waited on the stuck terminal")
	}
	release()
	task.Done("")
}

// A narrated task leaves a trail on plain output: a header, each note as
// it arrives, and the settled line, without replaying the notes on failure.
func TestTaskNarratedPlainOutput(t *testing.T) {
	t.Parallel()
	var out sink
	tasks := &Tasks{Out: &out, Style: &Style{}}

	upload := tasks.Start("Upload web to registry.example.com")
	upload.Note("pushing registry.example.com/web:1 (sha256:4b8b)")
	require.Empty(t, out.String(), "nothing prints before narration starts")
	upload.Narrate()
	upload.Progress(TaskProgress{Fraction: 0.5, HasFraction: true, Status: "50%"})
	upload.Note("sent 5.0MiB of 10.0MiB (50%) at 1.0MiB/s")
	upload.Done("4b8b0b4d722c, sent 10.0MiB")

	failing := tasks.Start("Import nginx:1.27")
	failing.Narrate()
	failing.Note("upload failed after 3s with 1.0MiB of 4.0MiB sent")
	failing.Fail()

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	require.Equal(t, []string{
		"  run   Upload web to registry.example.com",
		"        pushing registry.example.com/web:1 (sha256:4b8b)",
		"        sent 5.0MiB of 10.0MiB (50%) at 1.0MiB/s",
		"  ok    Upload web to registry.example.com  4b8b0b4d722c, sent 10.0MiB",
		"  run   Import nginx:1.27",
		"        upload failed after 3s with 1.0MiB of 4.0MiB sent",
		"  fail  Import nginx:1.27",
	}, lines)
}

// On a styled terminal a progress display replaces the note: a bar with
// its status, and the detail line below.
func TestTaskProgressRendersBar(t *testing.T) {
	t.Parallel()
	var out sink
	tasks := &Tasks{Out: &out, Style: &Style{Enabled: true}}
	task := tasks.Start("Upload web to registry.example.com")
	task.Narrate()
	task.Note("pushing registry.example.com/web:1")
	task.Progress(TaskProgress{Fraction: 0.5, HasFraction: true, Status: " 50%  5.0MiB / 10.0MiB",
		Detail: "no data sent for 7s"})

	tasks.mu.Lock()
	block := task.render()
	live := task.live
	tasks.mu.Unlock()
	require.Equal(t, 3, live)
	require.Contains(t, block, strings.Repeat("━", progressBarWidth/2))
	require.Contains(t, block, " 50%  5.0MiB / 10.0MiB")
	require.Contains(t, block, "no data sent for 7s")
	require.NotContains(t, block, "pushing registry.example.com/web:1", "the bar replaces the note")

	task.Progress(TaskProgress{Status: "preparing the image"})
	tasks.mu.Lock()
	block = task.render()
	live = task.live
	tasks.mu.Unlock()
	require.Equal(t, 2, live)
	require.NotContains(t, block, "━", "no bar without a known fraction")

	task.Done("")
	require.NotContains(t, out.String(), "run ", "narration is plain-output only")
}

// A narrow terminal drops the bar instead of wrapping the line, which
// would leave stale rows behind on the next frame.
func TestTaskProgressFitsNarrowTerminals(t *testing.T) {
	t.Parallel()
	var out sink
	tasks := &Tasks{Out: &out, Style: &Style{Enabled: true}, width: 30}
	task := tasks.Start("Upload web to registry.example.com")
	task.Progress(TaskProgress{Fraction: 0.5, HasFraction: true, Status: " 50%  5.0MiB / 10.0MiB  1.0MiB/s  ETA 5s"})
	tasks.mu.Lock()
	block := task.render()
	tasks.mu.Unlock()
	require.NotContains(t, block, "━")
	for _, line := range strings.Split(strings.TrimRight(ansi.Strip(block), "\n"), "\n") {
		require.LessOrEqual(t, ansi.StringWidth(line), 30, "line %q", line)
	}
	task.Done("")
}
