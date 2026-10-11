//go:build darwin || linux

package main

import (
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/client"
)

// attachTerminal runs attachRunMode against a pseudo-terminal that serves
// both stdin (for keys) and the output, and returns the master side plus
// a growing transcript of everything rendered.
type attachTerminal struct {
	master *os.File
	mu     sync.Mutex
	text   strings.Builder
}

func newAttachTerminal(t *testing.T) (*attachTerminal, *os.File) {
	t.Helper()
	master, slave, err := pty.Open()
	require.NoError(t, err)
	require.NoError(t, pty.Setsize(slave, &pty.Winsize{Rows: 30, Cols: 100}))
	term := &attachTerminal{master: master}
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, err := master.Read(buffer)
			if n > 0 {
				term.mu.Lock()
				term.text.Write(buffer[:n])
				term.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	previous := os.Stdin
	os.Stdin = slave
	t.Cleanup(func() {
		os.Stdin = previous
		_ = slave.Close()
		_ = master.Close()
	})
	return term, slave
}

func (term *attachTerminal) waitFor(t *testing.T, text string) {
	t.Helper()
	require.Eventually(t, func() bool {
		term.mu.Lock()
		defer term.mu.Unlock()
		return strings.Contains(term.text.String(), text)
	}, 5*time.Second, 10*time.Millisecond, "waiting for %q", text)
}

func TestAttachRunDetachKeyAndDoubleInterrupt(t *testing.T) {
	// Keys and signals are process-wide; this test owns them.
	f := newFakeRuns(t)
	f.seed(nil, map[string]client.Run{"r1": {ID: "r1", Kind: "deployment", Status: "running"}})

	t.Run("d detaches", func(t *testing.T) {
		term, slave := newAttachTerminal(t)
		type result struct {
			status string
			err    error
		}
		done := make(chan result, 1)
		go func() {
			outcome, err := attachRunMode(context.Background(), slave, f.client(), "r1", "", attachCancelsRun)
			done <- result{outcome.Status, err}
		}()
		term.waitFor(t, "d detaches, Ctrl-C cancels")
		_, err := io.WriteString(term.master, "d")
		require.NoError(t, err)
		got := <-done
		require.NoError(t, got.err)
		require.Equal(t, "detached", got.status)
		term.waitFor(t, "detached from run r1; the deployment continues on the server")
		require.Empty(t, f.cancelled())
	})

	t.Run("second interrupt cancels", func(t *testing.T) {
		term, slave := newAttachTerminal(t)
		type result struct {
			status string
			err    error
		}
		done := make(chan result, 1)
		go func() {
			outcome, err := attachRunMode(context.Background(), slave, f.client(), "r1", "", attachCancelsRun)
			done <- result{outcome.Status, err}
		}()
		term.waitFor(t, "d detaches, Ctrl-C cancels")
		require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGINT))
		term.waitFor(t, "press Ctrl-C again to cancel the run")
		require.Empty(t, f.cancelled(), "one press only arms")
		require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGINT))
		term.waitFor(t, "cancelling the run, waiting for the server")
		require.Eventually(t, func() bool {
			f.mu.Lock()
			defer f.mu.Unlock()
			return len(f.cancels) == 1
		}, 5*time.Second, 10*time.Millisecond)
		// The server reports the cancellation on the next poll.
		f.seed(nil, map[string]client.Run{"r1": {ID: "r1", Kind: "deployment", Status: "cancelled"}})
		got := <-done
		require.NoError(t, got.err)
		require.Equal(t, "cancelled", got.status)
	})
}

// frames counts the synchronized-output frames the renderer has written.
func (term *attachTerminal) frames() int {
	term.mu.Lock()
	defer term.mu.Unlock()
	return strings.Count(term.text.String(), "\x1b[?2026h")
}

// The observer owns the network and the view owns the terminal: a poll
// stuck on the server must not freeze the spinner, the elapsed timers, or
// the keys, and a detach drops the stuck request.
func TestAttachViewStaysLiveWhileTheServerStalls(t *testing.T) {
	type result struct {
		status string
		err    error
	}
	started := time.Now().Add(-5 * time.Second)

	t.Run("a stalled poll", func(t *testing.T) {
		f := newStallingRuns(t, client.Run{ID: "r1", Kind: "deployment", Status: "running"},
			client.Step{ID: "s1", Key: "rollout", Title: "Roll out api", Status: "running", StartedAt: &started})
		f.mu.Lock()
		f.stallPolls = true
		f.mu.Unlock()
		term, slave := newAttachTerminal(t)
		done := make(chan result, 1)
		go func() {
			outcome, err := attachRunMode(context.Background(), slave, f.client(), "r1", "", attachCancelsRun)
			done <- result{outcome.Status, err}
		}()
		term.waitFor(t, "Roll out api")
		receiveWithin(t, f.stalled, 5*time.Second, "the second poll to stall")

		frames := term.frames()
		require.Eventually(t, func() bool { return term.frames() >= frames+4 },
			5*time.Second, 10*time.Millisecond, "the spinner keeps animating")
		// The step started 5s before the attach; its elapsed time keeps
		// counting while the server says nothing new.
		term.waitFor(t, "7s")

		_, err := io.WriteString(term.master, "d")
		require.NoError(t, err)
		select {
		case got := <-done:
			require.NoError(t, got.err)
			require.Equal(t, "detached", got.status)
		case <-time.After(2 * time.Second):
			t.Fatal("the detach key waited on the stalled poll")
		}
		receiveWithin(t, f.aborted, 2*time.Second, "the detach to drop the stalled poll")
	})

	t.Run("a stalled cancel request", func(t *testing.T) {
		f := newStallingRuns(t, client.Run{ID: "r1", Kind: "deployment", Status: "running"},
			client.Step{ID: "s1", Key: "rollout", Title: "Roll out api", Status: "running", StartedAt: &started})
		f.mu.Lock()
		f.stallCancel = true
		f.continues = true
		f.mu.Unlock()
		term, slave := newAttachTerminal(t)
		done := make(chan result, 1)
		go func() {
			outcome, err := attachRunMode(context.Background(), slave, f.client(), "r1", "", attachCancelsRun)
			done <- result{outcome.Status, err}
		}()
		term.waitFor(t, "d detaches, Ctrl-C cancels")
		require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGINT))
		term.waitFor(t, "press Ctrl-C again to cancel the run")
		require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGINT))
		receiveWithin(t, f.stalled, 5*time.Second, "the cancel request to stall")
		term.waitFor(t, "cancelling the run, waiting for the server")

		frames := term.frames()
		require.Eventually(t, func() bool { return term.frames() >= frames+4 },
			5*time.Second, 10*time.Millisecond, "the spinner keeps animating")

		// The run settles before the server answers the cancel request;
		// the closing note still needs that answer.
		f.setStatus("cancelled")
		select {
		case got := <-done:
			t.Fatalf("finished before the cancel answer arrived: %+v", got)
		case <-time.After(time.Second):
		}
		f.release()
		got := <-done
		require.NoError(t, got.err)
		require.Equal(t, "cancelled", got.status)
		term.waitFor(t, "this was the environment's first deployment")
	})

	t.Run("a session interrupt during a stalled poll", func(t *testing.T) {
		f := newStallingRuns(t, client.Run{ID: "r1", Kind: "deployment", Status: "running"})
		f.mu.Lock()
		f.stallPolls = true
		f.mu.Unlock()
		term, slave := newAttachTerminal(t)
		done := make(chan result, 1)
		go func() {
			outcome, err := attachRunMode(context.Background(), slave, f.client(), "r1", "", attachSessionEnds)
			done <- result{outcome.Status, err}
		}()
		term.waitFor(t, "d detaches, Ctrl-C ends the session")
		receiveWithin(t, f.stalled, 5*time.Second, "the second poll to stall")
		require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGINT))
		select {
		case got := <-done:
			require.NoError(t, got.err)
			require.Equal(t, "detached", got.status)
		case <-time.After(2 * time.Second):
			t.Fatal("the session interrupt waited on the stalled poll")
		}
		receiveWithin(t, f.aborted, 2*time.Second, "the interrupt to drop the stalled poll")
	})
}
