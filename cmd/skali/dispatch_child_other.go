//go:build !unix

package main

import (
	"os"
	"syscall"
)

func relaySignals(*os.Process) (stop func()) { return func() {} }

func exitStatusOf(state *os.ProcessState) childStatus {
	return childStatus{Code: state.ExitCode()}
}

func reraise(syscall.Signal) {}
