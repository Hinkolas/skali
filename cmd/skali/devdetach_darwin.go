package main

import "golang.org/x/sys/unix"

// termios ioctls under the BSD names macOS uses.
const (
	ioctlReadTermios  = unix.TIOCGETA
	ioctlWriteTermios = unix.TIOCSETA
)
