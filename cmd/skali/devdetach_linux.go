package main

import "golang.org/x/sys/unix"

// termios ioctls under the names Linux uses.
const (
	ioctlReadTermios  = unix.TCGETS
	ioctlWriteTermios = unix.TCSETS
)
