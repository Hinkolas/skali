//go:build darwin || linux

package devremote

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func freeLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	return listener.Addr().String()
}

// runHelperInProcess stands in for sudo: it runs the helper's side in this
// process, as the CLI's sudo would run it in another one.
func runHelperInProcess(t *testing.T) func(context.Context, []string) error {
	return func(_ context.Context, args []string) error {
		require.Equal(t, ListenHelperArg, args[0])
		return ListenHelper(args[1], args[2:])
	}
}

func TestListenThroughHandsOverTheListeners(t *testing.T) {
	routes := []Route{
		{Scheme: "https", Listen: freeLoopbackAddress(t)},
		{Scheme: "http", Listen: freeLoopbackAddress(t)},
	}
	listeners, err := ListenThrough(t.Context(), routes, runHelperInProcess(t))
	require.NoError(t, err)
	require.Len(t, listeners, 2)
	for index, listener := range listeners {
		defer listener.Close()
		require.Equal(t, routes[index].Listen, listener.Addr().String(), "in route order")
		accepted := make(chan error, 1)
		go func() {
			conn, err := listener.Accept()
			if err == nil {
				conn.Close()
			}
			accepted <- err
		}()
		conn, err := net.Dial("tcp", routes[index].Listen)
		require.NoError(t, err)
		conn.Close()
		require.NoError(t, <-accepted, "the handed-over socket accepts connections")
	}
}

func TestListenHelperProcess(t *testing.T) {
	if os.Getenv("SKALI_TEST_LISTEN_HELPER") != "1" {
		return
	}
	for index, arg := range os.Args {
		if arg == ListenHelperArg {
			require.NoError(t, ListenHelper(os.Args[index+1], os.Args[index+2:]))
			return
		}
	}
	t.Fatal("missing listen helper arguments")
}

// Accept alone can succeed even when the handed-over socket immediately
// closes every connection. Exercise traffic in both directions after the
// helper has exited, on both routes and on repeated connections.
func TestListenThroughRelaysTraffic(t *testing.T) {
	for _, subprocess := range []bool{false, true} {
		name := "in-process"
		if subprocess {
			name = "subprocess"
		}
		t.Run(name, func(t *testing.T) {
			upstream := echoUpstream(t)
			routes := []Route{
				{Scheme: "https", Listen: freeLoopbackAddress(t), Target: upstream.Addr().String()},
				{Scheme: "http", Listen: freeLoopbackAddress(t), Target: upstream.Addr().String()},
			}
			run := runHelperInProcess(t)
			if subprocess {
				run = func(ctx context.Context, args []string) error {
					command := exec.CommandContext(ctx, os.Args[0], append([]string{"-test.run=^TestListenHelperProcess$", "--"}, args...)...)
					command.Env = append(os.Environ(), "SKALI_TEST_LISTEN_HELPER=1")
					return command.Run()
				}
			}
			listeners, err := ListenThrough(t.Context(), routes, run)
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			go func() {
				defer close(done)
				Serve(ctx, routes, listeners, t.Logf, t.Logf)
			}()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("relay did not stop")
				}
			})
			for range 3 {
				for _, route := range routes {
					require.Equal(t, "echo:"+route.Scheme, exchange(t, route.Listen, route.Scheme))
				}
			}
		})
	}
}

func TestListenHelperWaitsForAcknowledgement(t *testing.T) {
	// Keep the Unix socket path below macOS's limit, including the test name.
	dir, err := os.MkdirTemp("", "handoff-")
	require.NoError(t, err)
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "s")
	server, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	require.NoError(t, err)
	defer server.Close()
	require.NoError(t, server.SetDeadline(time.Now().Add(5*time.Second)))
	address := freeLoopbackAddress(t)
	done := make(chan error, 1)
	go func() { done <- ListenHelper(path, []string{address}) }()
	conn, err := server.AcceptUnix()
	require.NoError(t, err)
	defer conn.Close()
	select {
	case err := <-done:
		t.Fatalf("helper exited before the receiver took the sockets: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	listeners, err := receiveListeners(conn, []Route{{Listen: address}})
	require.NoError(t, err)
	defer listeners[0].Close()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("helper did not exit after acknowledgement")
	}
}

func TestListenThroughCancellationReleasesReceivedSockets(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	address := freeLoopbackAddress(t)
	_, err := ListenThrough(ctx, []Route{{Listen: address}}, func(_ context.Context, args []string) error {
		err := ListenHelper(args[1], args[2:])
		cancel()
		return err
	})
	require.ErrorIs(t, err, context.Canceled)
	listener, err := net.Listen("tcp", address)
	require.NoError(t, err, "the cancelled handoff must release its received sockets")
	listener.Close()
}

func TestListenThroughRejectsWrongRouteWithoutWaitingForAcknowledgement(t *testing.T) {
	address, wrong := freeLoopbackAddress(t), freeLoopbackAddress(t)
	_, err := ListenThrough(t.Context(), []Route{{Listen: address}}, func(_ context.Context, args []string) error {
		return ListenHelper(args[1], []string{wrong})
	})
	require.ErrorContains(t, err, "it listens on "+wrong)
	listener, err := net.Listen("tcp", wrong)
	require.NoError(t, err, "a rejected handoff must release its sockets")
	listener.Close()
}

// An address the helper cannot bind comes back as Listen reports it, so
// the CLI's explanations for a port in use apply unchanged, and nothing
// stays bound.
func TestListenThroughReportsWhatTheHelperCouldNotBind(t *testing.T) {
	free := freeLoopbackAddress(t)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer occupied.Close()
	routes := []Route{{Scheme: "https", Listen: free}, {Scheme: "http", Listen: occupied.Addr().String()}}

	_, err = ListenThrough(t.Context(), routes, runHelperInProcess(t))
	bind, ok := errors.AsType[*BindError](err)
	require.True(t, ok, "got %v", err)
	require.Equal(t, routes[1], bind.Route)
	require.ErrorIs(t, err, syscall.EADDRINUSE)

	listener, err := net.Listen("tcp", free)
	require.NoError(t, err, "the address the helper did bind was released")
	listener.Close()
}

func TestListenThroughWithoutAHandoff(t *testing.T) {
	routes := []Route{{Scheme: "https", Listen: freeLoopbackAddress(t)}}
	denied := errors.New("sudo: 3 incorrect password attempts")
	_, err := ListenThrough(t.Context(), routes, func(context.Context, []string) error { return denied })
	handoff, ok := errors.AsType[*HandoffError](err)
	require.True(t, ok, "got %v", err)
	require.ErrorIs(t, handoff, denied)

	_, err = ListenThrough(t.Context(), routes, func(context.Context, []string) error { return nil })
	require.ErrorIs(t, err, errNoHandoff, "a helper that exits cleanly without connecting is no success")
}

// The helper runs as root, so it binds IP addresses with ports, one at a
// time, and nothing else.
func TestListenHelperRefusesAnythingButAddresses(t *testing.T) {
	socket := t.TempDir() + "/s"
	for _, addresses := range [][]string{
		nil,
		{"0.0.0.0:443"},
		{"[::]:443"},
		{"[::%lo0]:443"},
		{"127.0.0.1:0"},
		{"localhost:443"},
		{"127.0.0.1"},
		{"127.0.0.1:1", "127.0.0.1:2", "127.0.0.1:3", "127.0.0.1:4", "127.0.0.1:5"},
	} {
		require.Error(t, ListenHelper(socket, addresses), "%v", addresses)
	}
	_, err := os.Stat(socket)
	require.ErrorIs(t, err, os.ErrNotExist, "refused before it touched the socket")
}

func TestFileListenersChecksWhereEachListens(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	file, err := listener.(*net.TCPListener).File()
	require.NoError(t, err)
	defer file.Close()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	require.NoError(t, err)
	number, err := strconv.Atoi(port)
	require.NoError(t, err)

	listeners, err := FileListeners([]Route{{Listen: listener.Addr().String()}}, []*os.File{file})
	require.NoError(t, err)
	listeners[0].Close()

	_, err = FileListeners([]Route{{Listen: hostPort("127.0.0.1", number+1)}}, []*os.File{file})
	require.ErrorContains(t, err, "it listens on "+listener.Addr().String())
	_, err = FileListeners([]Route{{Listen: listener.Addr().String()}, {Listen: "127.0.0.1:80"}}, []*os.File{file})
	require.ErrorContains(t, err, "got 1 listening sockets for 2 ports")
}

// A scope given as an interface index is the same scope the system names
// the bound socket's by.
func TestSameZoneComparesInterfaces(t *testing.T) {
	interfaces, err := net.Interfaces()
	require.NoError(t, err)
	require.NotEmpty(t, interfaces)
	ifc := interfaces[0]
	require.True(t, sameZone(ifc.Name, strconv.Itoa(ifc.Index)))
	require.True(t, sameZone("", ""))
	require.False(t, sameZone(ifc.Name, ""))
	require.False(t, sameZone(ifc.Name, strconv.Itoa(ifc.Index+1000)))
	require.False(t, sameZone("no-such-interface", "no-such-interface0"))
}
