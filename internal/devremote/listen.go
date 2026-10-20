package devremote

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"sync"

	"github.com/Hinkolas/skali/internal/localdev"
)

// Route is one relayed port: where a session listens and where it
// forwards to.
type Route struct {
	// Scheme names the traffic for messages: https or http.
	Scheme string
	Listen string
	Target string
}

// ProxyRoutes listen on this machine's loopback edge ports, the ones
// https://<domain>.localhost resolves to, and forward to the remote host's
// web ports.
func ProxyRoutes(host string) []Route {
	return []Route{
		{Scheme: "https", Listen: hostPort("127.0.0.1", localdev.HTTPSPort()), Target: hostPort(host, RemoteHTTPSPort())},
		{Scheme: "http", Listen: hostPort("127.0.0.1", localdev.HTTPPort()), Target: hostPort(host, RemoteHTTPPort())},
	}
}

// ExposeRoutes listen on the given address's web ports and forward to the
// local platform's loopback edge.
func ExposeRoutes(addr netip.Addr) []Route {
	host := addr.String()
	return []Route{
		{Scheme: "https", Listen: hostPort(host, RemoteHTTPSPort()), Target: hostPort("127.0.0.1", localdev.HTTPSPort())},
		{Scheme: "http", Listen: hostPort(host, RemoteHTTPPort()), Target: hostPort("127.0.0.1", localdev.HTTPPort())},
	}
}

// BindError is a route whose listen address could not be bound; Err keeps
// the system error for errors.Is checks (EADDRINUSE, EACCES,
// EADDRNOTAVAIL).
type BindError struct {
	Route Route
	Err   error
}

func (e *BindError) Error() string { return fmt.Sprintf("listen on %s: %v", e.Route.Listen, e.Err) }
func (e *BindError) Unwrap() error { return e.Err }

// Listen binds every route, all or nothing.
func Listen(routes []Route) ([]net.Listener, error) {
	listeners := make([]net.Listener, 0, len(routes))
	for _, route := range routes {
		listener, err := net.Listen("tcp", route.Listen)
		if err != nil {
			for _, bound := range listeners {
				bound.Close()
			}
			return nil, &BindError{Route: route, Err: err}
		}
		listeners = append(listeners, listener)
	}
	return listeners, nil
}

// FileListeners turns descriptors this process was handed, by the listen
// helper or by the process that started it in the background, into the
// routes' listeners, in route order. Each has to listen where its route
// does: a descriptor for anything else would serve the wrong traffic. The
// files stay the caller's to close.
func FileListeners(routes []Route, files []*os.File) ([]net.Listener, error) {
	if len(files) != len(routes) {
		return nil, fmt.Errorf("devremote: got %d listening sockets for %d ports", len(files), len(routes))
	}
	listeners := make([]net.Listener, 0, len(routes))
	for index, route := range routes {
		listener, err := net.FileListener(files[index])
		if err == nil && !listensAt(listener, route.Listen) {
			listener.Close()
			err = fmt.Errorf("it listens on %s", listener.Addr())
		}
		if err != nil {
			for _, bound := range listeners {
				bound.Close()
			}
			return nil, fmt.Errorf("devremote: the socket handed over for %s: %w", route.Listen, err)
		}
		listeners = append(listeners, listener)
	}
	return listeners, nil
}

func listensAt(listener net.Listener, address string) bool {
	want, err := netip.ParseAddrPort(address)
	if err != nil {
		return false
	}
	tcp, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return false
	}
	got := tcp.AddrPort()
	return got.Port() == want.Port() &&
		got.Addr().Unmap().WithZone("") == want.Addr().Unmap().WithZone("") &&
		sameZone(got.Addr().Zone(), want.Addr().Zone())
}

// sameZone compares IPv6 scopes by interface: the system names the scope of
// a bound socket (en0) where the user may have passed its index (6).
func sameZone(a, b string) bool {
	if a == b {
		return true
	}
	indexA, okA := zoneIndex(a)
	indexB, okB := zoneIndex(b)
	return okA && okB && indexA == indexB
}

func zoneIndex(zone string) (int, bool) {
	if index, err := strconv.Atoi(zone); err == nil {
		return index, true
	}
	if ifc, err := net.InterfaceByName(zone); err == nil {
		return ifc.Index, true
	}
	return 0, false
}

// ListenHelperArg, as the first argument, makes the skali binary the
// listen helper (see ListenThrough) instead of the CLI.
const ListenHelperArg = "__dev-listen"

// HandoffError is a listen helper that handed over no sockets: it did not
// start, the authorization to run it failed, or it exited early. Err is
// what running it reported.
type HandoffError struct {
	Err error
}

func (e *HandoffError) Error() string { return e.Err.Error() }
func (e *HandoffError) Unwrap() error { return e.Err }

// errNoHandoff is a helper that exited successfully without connecting.
var errNoHandoff = errors.New("the listen helper exited without handing over the ports")

// Serve forwards every route on its listener until ctx ends.
func Serve(ctx context.Context, routes []Route, listeners []net.Listener, logf, debugf func(format string, args ...any)) {
	var forwarders sync.WaitGroup
	for index, route := range routes {
		forwarder := &Forwarder{Target: route.Target, Logf: logf, Debugf: debugf}
		forwarders.Go(func() { _ = forwarder.Serve(ctx, listeners[index]) })
	}
	forwarders.Wait()
}
