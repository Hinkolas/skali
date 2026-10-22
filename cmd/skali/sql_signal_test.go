//go:build darwin || linux

package main

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/client"
)

func TestRunSessionInterrupt(t *testing.T) {
	open := func(ctx context.Context) (*client.ExecSession, error) {
		// runSession listens for signals before it opens.
		require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGINT))
		return blockingOpen(ctx)
	}
	err := runSession(context.Background(), sessionDrive{}, open, noReauth)
	exit, ok := errors.AsType[*client.ExecExitError](err)
	require.True(t, ok, "an interrupted session must not read as success: %v", err)
	require.Equal(t, 130, exit.Code)
}
