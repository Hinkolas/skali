package netwait

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAddress(t *testing.T) {
	cases := map[string]string{
		"db.example:5432":               "db.example:5432",
		"http://s3.platform.svc:8333":   "s3.platform.svc:8333",
		"http://s3.platform.svc":        "s3.platform.svc:80",
		"https://s3.example.com/path":   "s3.example.com:443",
		"postgres://u:p@db.example/app": "db.example:5432",
		"[fd00::1]:5432":                "[fd00::1]:5432",
		"http://[fd00::1]:8333/":        "[fd00::1]:8333",
	}
	for target, want := range cases {
		got, err := Address(target)
		require.NoError(t, err, target)
		require.Equal(t, want, got, target)
	}
	for _, target := range []string{"$(SKALI_WAIT_0_HOST):$(SKALI_WAIT_0_PORT)", "$(SKALI_WAIT_1_ENDPOINT)", "db.example", ":5432", "db.example:", "ftp://host", "http://:80"} {
		_, err := Address(target)
		require.Error(t, err, target)
	}
}

func TestWaitReachesOpenPort(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.Empty(t, Wait(ctx, []string{listener.Addr().String()}))
}

func TestWaitWaitsForLatePort(t *testing.T) {
	address := freeAddress(t)
	go func() {
		time.Sleep(700 * time.Millisecond)
		listener, err := net.Listen("tcp", address)
		if err != nil {
			return
		}
		t.Cleanup(func() { listener.Close() })
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started := time.Now()
	require.Empty(t, Wait(ctx, []string{address}))
	require.GreaterOrEqual(t, time.Since(started), 500*time.Millisecond)
}

func TestWaitReportsUnreachable(t *testing.T) {
	open, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer open.Close()
	closed := freeAddress(t)

	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	require.Equal(t, []string{closed}, Wait(ctx, []string{open.Addr().String(), closed}))
}

func TestRunFailsOpen(t *testing.T) {
	require.NoError(t, Run([]string{"--timeout", "600ms", freeAddress(t)}))
	// Unset outputs leave unexpanded or empty targets; they are skipped.
	started := time.Now()
	require.NoError(t, Run([]string{"--timeout", "5s", "", ":", "not-an-address", "$(SKALI_WAIT_0_HOST):$(SKALI_WAIT_0_PORT)"}))
	require.Less(t, time.Since(started), time.Second)
	require.Error(t, Run([]string{"--no-such-flag"}))
}

// freeAddress returns a loopback address nothing listens on.
func freeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	return address
}
