package devremote

import (
	"context"
	"fmt"
	"net"
	"net/netip"
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

// Serve forwards every route on its listener until ctx ends.
func Serve(ctx context.Context, routes []Route, listeners []net.Listener, logf func(format string, args ...any)) {
	var forwarders sync.WaitGroup
	for index, route := range routes {
		forwarder := &Forwarder{Target: route.Target, Logf: logf}
		forwarders.Go(func() { _ = forwarder.Serve(ctx, listeners[index]) })
	}
	forwarders.Wait()
}
