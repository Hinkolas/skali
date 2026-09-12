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
			status, err := attachRunMode(context.Background(), slave, f.client(), "r1", "", attachCancelsRun)
			done <- result{status, err}
		}()
		term.waitFor(t, "d detaches, Ctrl-C cancels")
		_, err := io.WriteString(term.master, "d")
		require.NoError(t, err)
		got := <-done
		require.NoError(t, got.err)
		require.Equal(t, "detached", got.status)
		term.waitFor(t, "detached from run r1; the deployment continues on the server")
		require.Empty(t, f.cancels)
	})

	t.Run("second interrupt cancels", func(t *testing.T) {
		term, slave := newAttachTerminal(t)
		type result struct {
			status string
			err    error
		}
		done := make(chan result, 1)
		go func() {
			status, err := attachRunMode(context.Background(), slave, f.client(), "r1", "", attachCancelsRun)
			done <- result{status, err}
		}()
		term.waitFor(t, "d detaches, Ctrl-C cancels")
		require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGINT))
		term.waitFor(t, "press Ctrl-C again to cancel the run")
		require.Empty(t, f.cancels, "one press only arms")
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
