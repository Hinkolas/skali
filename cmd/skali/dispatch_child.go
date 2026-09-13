package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// childStatus is how a dispatched child ended: its exit status, or the
// signal that killed it (Code is then 128 plus the signal number, the
// shell convention).
type childStatus struct {
	Code   int
	Signal syscall.Signal
}

// spawnChild runs a cached skali binary as a child with this process's
// stdio, waits for it, and reports how it ended. The child inherits the
// file descriptors themselves (no pipes), the working directory, and the
// process group: raw-mode commands own the real terminal, and terminal
// signals reach the child directly. Start is retried on ETXTBSY for a
// binary written a moment ago.
func spawnChild(ctx context.Context, path string, args, env []string) (childStatus, error) {
	var cmd *exec.Cmd
	err := retryTextFileBusy(ctx, func() error {
		cmd = exec.Command(path, args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		cmd.Env = env
		return cmd.Start()
	})
	if err != nil {
		return childStatus{}, err
	}
	stop := relaySignals(cmd.Process)
	waitErr := cmd.Wait()
	stop()
	var exit *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exit) {
		return childStatus{}, waitErr
	}
	return exitStatusOf(cmd.ProcessState), nil
}

// childEnv is the environment a dispatched child runs with: everything
// this process has, plus the marker that stops it from dispatching again.
func childEnv(environ []string) []string {
	env := make([]string, 0, len(environ)+1)
	for _, entry := range environ {
		if strings.HasPrefix(entry, envDispatched+"=") {
			continue
		}
		env = append(env, entry)
	}
	return append(env, envDispatched+"=1")
}
