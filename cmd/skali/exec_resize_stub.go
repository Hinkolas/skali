//go:build !unix

package main

import "github.com/Hinkolas/skali/internal/client"

// watchResize is a no-op without SIGWINCH: the initial size still applies,
// later resizes are not propagated.
func watchResize(resize chan<- client.TerminalSize) (stop func()) {
	return func() {}
}
