//go:build darwin || linux

package main

import (
	"os"

	"golang.org/x/sys/unix"

	"github.com/Hinkolas/skali/internal/clirender"
)

// watchDetachKey arms the dev session's detach key: stdin switches to
// cbreak mode (no echo, no line buffering; signal keys like Ctrl-C keep
// their meaning, unlike full raw mode) and the returned channel closes when
// the user presses d (or Ctrl-D, for shell muscle memory). The channel is
// nil when stdin is not a terminal or cannot be switched, so pipes and e2e
// runs keep their plain behavior. restore returns the terminal to its
// previous mode; it is safe to call in every case.
func watchDetachKey() (detach <-chan struct{}, restore func()) {
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
	pressed := make(chan struct{})
	go func() {
		buffer := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(buffer)
			if err != nil {
				return
			}
			if n == 1 && (buffer[0] == 'd' || buffer[0] == 'D' || buffer[0] == 0x04) {
				close(pressed)
				return
			}
		}
	}()
	return pressed, func() { _ = unix.IoctlSetTermios(fd, ioctlWriteTermios, previous) }
}
