//go:build unix

package main

import (
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"

	"github.com/Hinkolas/skali/internal/client"
)

// watchResize forwards local terminal size changes into resize until the
// returned stop function is called.
func watchResize(resize chan<- client.TerminalSize) (stop func()) {
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-winch:
				cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
				if err != nil {
					continue
				}
				select {
				case resize <- client.TerminalSize{Cols: uint16(cols), Rows: uint16(rows)}:
				default: // drop bursts; the freshest size follows shortly
				}
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(winch)
		close(done)
	}
}
