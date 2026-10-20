//go:build darwin || linux

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Hinkolas/skali/internal/devremote"
)

// envReadyFD names the descriptor a background session reports on once it
// serves; set only for the child startDetached spawns.
const envReadyFD = "SKALI_DEV_READY_FD"

// envListenFDs names the descriptors of the listening sockets a background
// session serves on, in route order; set only for the child startDetached
// spawns.
const envListenFDs = "SKALI_DEV_LISTEN_FDS"

// detachedStartTimeout bounds how long a background session may take to
// come up: it fetches the remote CA again before it serves.
const detachedStartTimeout = time.Minute

// startDetached runs this binary with args as a background process in a
// session of its own, so closing the terminal does not end it, with its
// output in logPath. The process serves on listeners, which this one
// bound: it has no terminal to ask for a password, and the ports cannot
// be taken in between. It returns once the process reports that it
// serves; a process that exits first fails with the end of its log.
func startDetached(ctx context.Context, logPath string, args []string, listeners []net.Listener) (int, error) {
	executable, err := os.Executable()
	if err != nil {
		return 0, err
	}
	logFile, err := openSessionLog(logPath)
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
	sockets, err := listenerFiles(listeners)
	if err != nil {
		readyWrite.Close()
		return 0, err
	}
	defer func() {
		for _, socket := range sockets {
			socket.Close()
		}
	}()

	if verboseTranscript {
		args = append(slices.Clone(args), "--verbose")
	}
	command := exec.Command(executable, args...)
	command.Dir = "/"
	command.Stdin = devNull
	command.Stdout = logFile
	command.Stderr = logFile
	// The ready pipe is descriptor 3 in the child, the sockets follow it.
	command.ExtraFiles = append([]*os.File{readyWrite}, sockets...)
	command.Env = append(os.Environ(), envReadyFD+"=3")
	if len(sockets) > 0 {
		descriptors := make([]string, len(sockets))
		for index := range sockets {
			descriptors[index] = strconv.Itoa(4 + index)
		}
		command.Env = append(command.Env, envListenFDs+"="+strings.Join(descriptors, ","))
	}
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

// listenerFiles duplicates listeners' sockets for a child to inherit.
func listenerFiles(listeners []net.Listener) ([]*os.File, error) {
	files := make([]*os.File, 0, len(listeners))
	for _, listener := range listeners {
		tcp, ok := listener.(*net.TCPListener)
		var file *os.File
		var err error
		if ok {
			file, err = tcp.File()
		} else {
			err = fmt.Errorf("cannot hand %s to a background process", listener.Addr())
		}
		if err != nil {
			for _, opened := range files {
				opened.Close()
			}
			return nil, err
		}
		files = append(files, file)
	}
	return files, nil
}

// inheritedListeners takes the listening sockets startDetached handed a
// background session; inherited is false for any other process, which
// binds its routes itself.
func inheritedListeners(routes []devremote.Route) (listeners []net.Listener, inherited bool, err error) {
	value, ok := os.LookupEnv(envListenFDs)
	if !ok {
		return nil, false, nil
	}
	_ = os.Unsetenv(envListenFDs)
	var files []*os.File
	defer func() {
		for _, file := range files {
			file.Close()
		}
	}()
	for _, field := range strings.Split(value, ",") {
		descriptor, err := strconv.Atoi(field)
		if err != nil || descriptor < 3 {
			return nil, true, fmt.Errorf("%s=%q does not name inherited sockets", envListenFDs, value)
		}
		files = append(files, os.NewFile(uintptr(descriptor), "listener"))
	}
	listeners, err = devremote.FileListeners(routes, files)
	return listeners, true, err
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

// openSessionLog truncates a background session's log, creating the state
// directory on a machine that has none yet.
func openSessionLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
}
