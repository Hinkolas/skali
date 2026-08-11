//go:build !(darwin || linux)

package main

// watchDetachKey is disarmed without termios: the session behaves as
// before, Ctrl-C ends it.
func watchDetachKey() (detach <-chan struct{}, restore func()) {
	return nil, func() {}
}
