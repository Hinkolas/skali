//go:build darwin || linux

package main

import (
	"os"
	"sync"

	"golang.org/x/sys/unix"

	"github.com/Hinkolas/skali/internal/clirender"
)

// keyReader is the process's one stdin key reader. Live views that take
// single keys (a run attach, the dev log follow) subscribe in turn: a
// goroutine blocked in Read cannot be stopped, so two readers would race
// for keystrokes and the loser's key would vanish. While a subscription is
// held stdin runs in cbreak mode: no echo, no line buffering, but signal
// keys keep their meaning, so Ctrl-C still raises SIGINT and Ctrl-Z still
// suspends, unlike full raw mode.
type keyReader struct {
	mu         sync.Mutex
	file       *os.File // the stdin the reading goroutine serves
	subscriber chan byte
}

var terminalKeys keyReader

// subscribe delivers every key pressed on stdin to the returned channel
// until release is called; release also restores the terminal mode. The
// channel is nil when stdin is not a terminal or cannot be switched, so
// pipes and e2e runs keep their plain behavior.
func (k *keyReader) subscribe() (keys <-chan byte, release func()) {
	if !clirender.IsTerminal(os.Stdin) {
		return nil, func() {}
	}
	fd := int(os.Stdin.Fd())
	previous, err := unix.IoctlGetTermios(fd, ioctlReadTermios)
	if err != nil {
		return nil, func() {}
	}
	cbreak := *previous
	cbreak.Lflag &^= unix.ICANON | unix.ECHO
	cbreak.Cc[unix.VMIN] = 1
	cbreak.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, ioctlWriteTermios, &cbreak); err != nil {
		return nil, func() {}
	}
	channel := make(chan byte, 8)
	k.mu.Lock()
	k.subscriber = channel
	if k.file != os.Stdin {
		// A new stdin (tests swap it for a pty) gets its own reader; the
		// old goroutine notices on its next key and ends.
		k.file = os.Stdin
		go k.read(os.Stdin)
	}
	k.mu.Unlock()
	return channel, func() {
		k.mu.Lock()
		if k.subscriber == channel {
			k.subscriber = nil
		}
		k.mu.Unlock()
		_ = unix.IoctlSetTermios(fd, ioctlWriteTermios, previous)
	}
}

func (k *keyReader) read(file *os.File) {
	buffer := make([]byte, 1)
	for {
		n, err := file.Read(buffer)
		if err != nil {
			return
		}
		k.mu.Lock()
		if k.file != file {
			k.mu.Unlock()
			return
		}
		if n == 1 && k.subscriber != nil {
			select {
			case k.subscriber <- buffer[0]:
			default: // a slow view drops keys rather than blocking stdin
			}
		}
		k.mu.Unlock()
	}
}

// isDetachKey is d (or Ctrl-D, for shell muscle memory).
func isDetachKey(key byte) bool { return key == 'd' || key == 'D' || key == 0x04 }

// watchDetachKey arms the dev session's detach key: the returned channel
// closes when the user presses d. Nil without a terminal; restore returns
// the terminal to its previous mode and is safe to call in every case.
func watchDetachKey() (detach <-chan struct{}, restore func()) {
	keys, release := terminalKeys.subscribe()
	if keys == nil {
		return nil, release
	}
	pressed := make(chan struct{})
	done := make(chan struct{})
	go func() {
		for {
			select {
			case key := <-keys:
				if isDetachKey(key) {
					close(pressed)
					return
				}
			case <-done:
				return
			}
		}
	}()
	return pressed, func() { close(done); release() }
}
