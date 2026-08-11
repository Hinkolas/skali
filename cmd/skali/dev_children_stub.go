//go:build !darwin && !linux

package main

import "os/exec"

// Process groups and unix signals are unavailable here; children terminate
// hard. The dev loop degrades but keeps compiling (the local platform
// itself is darwin/linux only).
func configureChildProcess(cmd *exec.Cmd) { cmd.WaitDelay = childGrace }

func terminateChild(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func killChild(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
