//go:build !(darwin || linux)

package main

// keyReader is disarmed without termios: live views take no keys and
// Ctrl-C keeps its plain meaning.
type keyReader struct{}

var terminalKeys keyReader

func (keyReader) subscribe() (keys <-chan byte, release func()) { return nil, func() {} }

func isDetachKey(key byte) bool { return key == 'd' || key == 'D' || key == 0x04 }

// watchDetachKey is disarmed without termios: the session behaves as
// before, Ctrl-C ends it.
func watchDetachKey() (detach <-chan struct{}, restore func()) {
	return nil, func() {}
}
