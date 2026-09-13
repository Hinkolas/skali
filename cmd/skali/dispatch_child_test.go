//go:build unix

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSpawnChildInheritsEnvAndArgs(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "seen")
	script := writeExecutable(t, dir, "child", []byte("#!/bin/sh\nprintf '%s|%s|%s\\n' \"$*\" \"$SKALI_DISPATCHED\" \"$MARK\" > \"$OUT\"\nexit 0\n"))
	env := childEnv(append(os.Environ(), "OUT="+out, "MARK=yes", envDispatched+"=stale"))

	status, err := spawnChild(context.Background(), script, []string{"a", "b c"}, env)
	require.NoError(t, err)
	require.Equal(t, childStatus{Code: 0}, status)
	seen, err := os.ReadFile(out)
	require.NoError(t, err)
	require.Equal(t, "a b c|1|yes\n", string(seen))
}

func TestSpawnChildExitStatus(t *testing.T) {
	dir := t.TempDir()
	seven := writeExecutable(t, dir, "seven", []byte("#!/bin/sh\nexit 7\n"))
	status, err := spawnChild(context.Background(), seven, nil, os.Environ())
	require.NoError(t, err)
	require.Equal(t, childStatus{Code: 7}, status)

	killed := writeExecutable(t, dir, "killed", []byte("#!/bin/sh\nkill -TERM $$\n"))
	status, err = spawnChild(context.Background(), killed, nil, os.Environ())
	require.NoError(t, err)
	require.Equal(t, childStatus{Code: 143, Signal: syscall.SIGTERM}, status)

	_, err = spawnChild(context.Background(), filepath.Join(dir, "missing"), nil, os.Environ())
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestExitStatusOf(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 7")
	require.Error(t, cmd.Run())
	require.Equal(t, childStatus{Code: 7}, exitStatusOf(cmd.ProcessState))

	cmd = exec.Command("sh", "-c", "kill -TERM $$")
	require.Error(t, cmd.Run())
	require.Equal(t, childStatus{Code: 143, Signal: syscall.SIGTERM}, exitStatusOf(cmd.ProcessState))
}

func TestChildEnvReplacesMarker(t *testing.T) {
	env := childEnv([]string{"A=1", envDispatched + "=old", "B=2"})
	require.Equal(t, []string{"A=1", "B=2", envDispatched + "=1"}, env)
}
