package clirender

import (
	"strings"
	"sync"
	"testing"
	"time"

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
