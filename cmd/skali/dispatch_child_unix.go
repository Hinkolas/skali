//go:build unix

package main

import (
	"os"
	"os/signal"
	"syscall"
)

// relaySignals makes the parent transparent while the child runs. Terminal
// signals (Ctrl-C, Ctrl-\, hangup, window resize, job control) already
// reach the child through the shared foreground process group, so the
// parent only has to survive the ones that would kill it; signals aimed at
// the parent's pid alone (kill from a script) are forwarded. The returned
// stop restores the defaults.
func relaySignals(process *os.Process) (stop func()) {
	signal.Ignore(syscall.SIGINT, syscall.SIGQUIT)
	forwarded := make(chan os.Signal, 4)
	signal.Notify(forwarded, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGUSR1, syscall.SIGUSR2)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case sig := <-forwarded:
				_ = process.Signal(sig)
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(forwarded)
		signal.Reset(syscall.SIGINT, syscall.SIGQUIT)
		close(done)
	}
}

// exitStatusOf maps a finished child onto childStatus.
func exitStatusOf(state *os.ProcessState) childStatus {
	if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return childStatus{Code: 128 + int(status.Signal()), Signal: status.Signal()}
	}
	return childStatus{Code: state.ExitCode()}
}

// reraise ends this process the way the child ended when a terminal or
// hangup signal killed it, so the shell observes the same termination; the
// caller exits with the numeric code if the process survives.
func reraise(sig syscall.Signal) {
	switch sig {
	case syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGPIPE:
		signal.Reset(sig)
		_ = syscall.Kill(os.Getpid(), sig)
	}
}
