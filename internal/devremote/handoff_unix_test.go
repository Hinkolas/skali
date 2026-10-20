//go:build darwin || linux

package devremote

import (
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"syscall"
	"testing"

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
