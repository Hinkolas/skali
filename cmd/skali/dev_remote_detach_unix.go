//go:build darwin || linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// envReadyFD names the descriptor a background session reports on once it
// serves; set only for the child startDetached spawns.
const envReadyFD = "SKALI_DEV_READY_FD"

// detachedStartTimeout bounds how long a background session may take to
// come up: it fetches the remote CA again before it serves.
const detachedStartTimeout = time.Minute

// startDetached runs this binary with args as a background process in a
// session of its own, so closing the terminal does not end it, with its
// output in logPath. It returns once the process reports that it serves;
// a process that exits first fails with the end of its log.
func startDetached(ctx context.Context, logPath string, args []string) (int, error) {
	executable, err := os.Executable()
	if err != nil {
		return 0, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	defer logFile.Close()
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return 0, err
	}
	defer devNull.Close()
	readyRead, readyWrite, err := os.Pipe()
	if err != nil {
		return 0, err
	}
	defer readyRead.Close()

	command := exec.Command(executable, args...)
	command.Dir = "/"
	command.Stdin = devNull
	command.Stdout = logFile
	command.Stderr = logFile
	command.ExtraFiles = []*os.File{readyWrite} // descriptor 3 in the child
	command.Env = append(os.Environ(), envReadyFD+"=3")
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err = command.Start()
	readyWrite.Close()
	if err != nil {
		return 0, err
	}
	exited := make(chan struct{})
	go func() {
		_ = command.Wait()
		close(exited)
	}()

	ready := make(chan bool, 1)
	go func() {
		buffer := make([]byte, 16)
		read, _ := readyRead.Read(buffer)
		ready <- strings.HasPrefix(string(buffer[:read]), "ready")
	}()
	select {
	case ok := <-ready:
		if ok {
			return command.Process.Pid, nil
		}
		// The pipe closed without the signal: the process is exiting.
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
		}
		return 0, detachedFailure(logPath)
	case <-time.After(detachedStartTimeout):
		_ = command.Process.Kill()
		return 0, fmt.Errorf("the background process did not come up within %s; its log is %s", detachedStartTimeout, logPath)
	case <-ctx.Done():
		_ = command.Process.Kill()
		return 0, ctx.Err()
	}
}

// signalDetachedReady tells the parent of a background session that it
// serves; a foreground run has no parent waiting and does nothing.
func signalDetachedReady() {
	value := os.Getenv(envReadyFD)
	if value == "" {
		return
	}
	_ = os.Unsetenv(envReadyFD)
	descriptor, err := strconv.Atoi(value)
	if err != nil {
		return
	}
	file := os.NewFile(uintptr(descriptor), "ready")
	if file == nil {
		return
	}
	_, _ = file.WriteString("ready\n")
	_ = file.Close()
}

// detachedFailure reports a background process that exited before it
// served, with the last lines of its log, which hold the error.
func detachedFailure(logPath string) error {
	raw, err := os.ReadFile(logPath)
	if err != nil || len(strings.TrimSpace(string(raw))) == 0 {
		return fmt.Errorf("the background process exited before it started serving; its log is %s", logPath)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) > 12 {
		lines = lines[len(lines)-12:]
	}
	return errors.New("the background process exited before it started serving:\n" + strings.Join(lines, "\n"))
}
