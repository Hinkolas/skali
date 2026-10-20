package devremote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// echoUpstream answers each connection with "echo:" and everything it read
// once the client half-closed, then closes: a request/response exchange
// that only works when the end of the client's stream is relayed.
func echoUpstream(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				data, err := io.ReadAll(conn)
				if err != nil {
					return
				}
				_, _ = conn.Write(append([]byte("echo:"), data...))
			}()
		}
	}()
	return listener
}

type logLines struct {
	mu    sync.Mutex
	lines []string
}

func (l *logLines) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logLines) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

// serveForwarder runs f on a fresh loopback listener and returns its
// address and a stop function that waits for Serve to return.
func serveForwarder(t *testing.T, f *Forwarder) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		require.NoError(t, f.Serve(ctx, listener))
	}()
	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Serve did not return after cancel")
		}
	}
	t.Cleanup(stop)
	return listener.Addr().String(), stop
}

func exchange(t *testing.T, address, payload string) string {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, 5*time.Second)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
	_, err = conn.Write([]byte(payload))
	require.NoError(t, err)
	require.NoError(t, conn.(*net.TCPConn).CloseWrite())
	reply, err := io.ReadAll(conn)
	require.NoError(t, err)
	return string(reply)
}

func TestForwarderRelaysBothDirectionsWithHalfClose(t *testing.T) {
	t.Parallel()
	upstream := echoUpstream(t)
	address, _ := serveForwarder(t, &Forwarder{Target: upstream.Addr().String()})
	require.Equal(t, "echo:ping", exchange(t, address, "ping"))
	large := strings.Repeat("0123456789", 100_000)
	require.Equal(t, "echo:"+large, exchange(t, address, large))
}

func TestForwarderConcurrentConnections(t *testing.T) {
	t.Parallel()
	upstream := echoUpstream(t)
	address, _ := serveForwarder(t, &Forwarder{Target: upstream.Addr().String()})
	var connections sync.WaitGroup
	for index := range 64 {
		connections.Go(func() {
			payload := fmt.Sprintf("connection-%d", index)
			if reply := exchange(t, address, payload); reply != "echo:"+payload {
				t.Errorf("connection %d got %q", index, reply)
			}
		})
	}
	connections.Wait()
}

// An unreachable target closes the client's connection right away and is
// reported once, not per connection; its return is reported too.
func TestForwarderReportsUnreachableTargetOnce(t *testing.T) {
	t.Parallel()
	upstream := echoUpstream(t)
	target := upstream.Addr().String()
	var reachable sync.Mutex
	up := false
	logs := &logLines{}
	address, _ := serveForwarder(t, &Forwarder{
		Target: target,
		Logf:   logs.logf,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			reachable.Lock()
			ok := up
			reachable.Unlock()
			if !ok {
				return nil, fmt.Errorf("connect: connection refused")
			}
			var dialer net.Dialer
			return dialer.DialContext(ctx, network, address)
		},
	})
	for range 3 {
		conn, err := net.Dial("tcp", address)
		require.NoError(t, err)
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
		_, err = conn.Read(make([]byte, 1))
		require.ErrorIs(t, err, io.EOF, "the client sees its connection closed")
		conn.Close()
	}
	require.Equal(t, []string{"cannot reach " + target + ": connect: connection refused"}, logs.snapshot())

	reachable.Lock()
	up = true
	reachable.Unlock()
	require.Equal(t, "echo:back", exchange(t, address, "back"))
	require.Equal(t, "reaching "+target+" again", logs.snapshot()[1])
	require.Len(t, logs.snapshot(), 2)
}

// Stopping closes the listener and every relayed connection, so a session
// ends promptly even with idle keep-alive connections open.
func TestForwarderStopClosesConnections(t *testing.T) {
	t.Parallel()
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer upstream.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := upstream.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	address, stop := serveForwarder(t, &Forwarder{Target: upstream.Addr().String()})
	client, err := net.Dial("tcp", address)
	require.NoError(t, err)
	defer client.Close()
	held := <-accepted
	defer held.Close()

	stop()
	require.NoError(t, client.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, err = client.Read(make([]byte, 1))
	require.Error(t, err)
	if netErr, ok := errors.AsType[net.Error](err); ok {
		require.False(t, netErr.Timeout(), "the connection was closed, not left hanging")
	}
	_, err = net.DialTimeout("tcp", address, time.Second)
	require.Error(t, err, "the listener is closed")
}

func TestForwarderReportsEmptyClientStreamOnlyInDebugLog(t *testing.T) {
	t.Parallel()
	upstream := echoUpstream(t)
	logs, debug := &logLines{}, &logLines{}
	address, _ := serveForwarder(t, &Forwarder{Target: upstream.Addr().String(), Logf: logs.logf, Debugf: debug.logf})
	require.Equal(t, "echo:", exchange(t, address, ""))
	require.Eventually(t, func() bool { return len(debug.snapshot()) == 1 }, time.Second, time.Millisecond)
	require.Contains(t, debug.snapshot()[0], "(client -> target) ended after 0 bytes: EOF")
	require.Contains(t, debug.snapshot()[0], upstream.Addr().String())
	require.Empty(t, logs.snapshot(), "an empty stream is not an unreachable target")
}

type brokenReadConn struct{ net.Conn }

func (brokenReadConn) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestForwarderReportsCopyFailureUnlessStopping(t *testing.T) {
	t.Parallel()
	for _, stopping := range []bool{false, true} {
		t.Run(fmt.Sprint(stopping), func(t *testing.T) {
			client, browser := net.Pipe()
			defer browser.Close()
			upstream, target := net.Pipe()
			defer target.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if stopping {
				cancel()
			}
			debug := &logLines{}
			f := Forwarder{
				Target: "remote:443", Debugf: debug.logf,
				Dial: func(context.Context, string, string) (net.Conn, error) { return upstream, nil },
			}
			f.relay(ctx, brokenReadConn{client})
			if stopping {
				require.Empty(t, debug.snapshot(), "stopping is not a relay failure")
			} else {
				require.Contains(t, strings.Join(debug.snapshot(), "\n"), "remote:443 (client -> target) ended after 0 bytes: unexpected EOF")
			}
		})
	}
}
