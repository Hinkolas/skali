//go:build darwin || linux

package main

import (
	"os/exec"
	"syscall"
)

// configureChildProcess puts the dev command in its own process group, so
// the terminal's Ctrl-C never reaches it directly (the session terminates
// children deliberately, in order, before the pause epilogue), and turns a
// context cancel into a group SIGTERM with a SIGKILL escalation after
// WaitDelay instead of Go's default immediate SIGKILL.
func configureChildProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return signalChildGroup(cmd, syscall.SIGTERM) }
	cmd.WaitDelay = childGrace
}

func signalChildGroup(cmd *exec.Cmd, signal syscall.Signal) error {
	if cmd.Process == nil {
		return nil
	}
	// The negative pid addresses the whole process group: a shell-wrapped
	// dev server's grandchildren terminate with it.
	return syscall.Kill(-cmd.Process.Pid, signal)
}

func terminateChild(cmd *exec.Cmd) { _ = signalChildGroup(cmd, syscall.SIGTERM) }
func killChild(cmd *exec.Cmd)      { _ = signalChildGroup(cmd, syscall.SIGKILL) }
