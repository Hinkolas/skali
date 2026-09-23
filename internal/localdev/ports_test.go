package localdev

import (
	"context"
	"errors"
	"net"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEdgePortCheckReportsOnlyAddressInUse(t *testing.T) {
	original := listenLoopback
	defer func() { listenLoopback = original }()
	ctx := context.Background()

	// A port another process holds is reported after the retries, naming
	// the port and the override.
	attempts := 0
	listenLoopback = func(port int) error {
		attempts++
		return &net.OpError{Op: "listen", Err: syscall.EADDRINUSE}
	}
	err := waitPortFreeAttempts(ctx, 8443, 2)
	require.ErrorContains(t, err, "port 8443 is in use")
	require.ErrorContains(t, err, "SKALI_DEV_HTTPS_PORT")
	require.Equal(t, 2, attempts)

	// A busy port that frees up within the retry window passes.
	attempts = 0
	listenLoopback = func(int) error {
		attempts++
		if attempts == 1 {
			return &net.OpError{Op: "listen", Err: syscall.EADDRINUSE}
		}
		return nil
	}
	require.NoError(t, waitPortFreeAttempts(ctx, 80, 2))

	// Permission denied (an unprivileged bind below 1024 on Linux) and any
	// other failure are not this check's business: docker can still bind.
	listenLoopback = func(int) error { return &net.OpError{Op: "listen", Err: syscall.EACCES} }
	require.NoError(t, waitPortFreeAttempts(ctx, 80, 2))
	listenLoopback = func(int) error { return errors.New("something else") }
	require.NoError(t, waitPortFreeAttempts(ctx, 80, 2))
	listenLoopback = func(int) error { return nil }
	require.NoError(t, checkEdgePortsFree(ctx))
}
