package localdev

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"syscall"
	"time"
)

// listenLoopback bind-tests one host port on the loopback address; tests
// swap it to simulate the platform-specific failures.
var listenLoopback = func(port int) error {
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return err
	}
	return listener.Close()
}

// checkEdgePortsFree refuses to create or start the cluster while another
// process holds the edge's host ports, naming the culprit's port instead
// of surfacing k3d's raw port-allocation error minutes later. Only an
// address-in-use answer counts: an unprivileged process on Linux cannot
// bind below 1024 at all (EACCES) even though the docker daemon can, and
// anything else is not this check's business. Docker Desktop's port proxy
// can hold a binding for a moment after a cluster stop, so a busy port is
// retried briefly before it is reported.
func checkEdgePortsFree(ctx context.Context) error {
	for _, port := range []int{HTTPPort(), HTTPSPort()} {
		if err := waitPortFree(ctx, port); err != nil {
			return err
		}
	}
	return nil
}

func waitPortFree(ctx context.Context, port int) error {
	return waitPortFreeAttempts(ctx, port, 3)
}

func waitPortFreeAttempts(ctx context.Context, port, attempts int) error {
	for attempt := 1; ; attempt++ {
		err := listenLoopback(port)
		if !errors.Is(err, syscall.EADDRINUSE) {
			return nil
		}
		if attempt == attempts {
			return fmt.Errorf("port %d is in use on this host, but the local platform publishes its edge there; "+
				"free it (lsof -nP -iTCP:%d -sTCP:LISTEN names the process) and run skali dev again. "+
				"SKALI_DEV_HTTP_PORT and SKALI_DEV_HTTPS_PORT move the edge to other ports, at the price of "+
				"https redirects and application origins that no longer match a cluster's", port, port)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
