// Package netwait is the release Job's reachability wait: `skalid
// net-wait [--timeout d] <target>...` runs as an init container and returns
// once every target accepts a TCP connection. A fresh pod's first
// connections to a fenced platform port can be refused while the CNI's
// network policy enforcement still registers the pod's address; init
// containers share the pod's address, so once the wait gets through, the
// release command does too.
//
// The wait fails open: when the timeout passes it logs the unreachable
// targets and returns nil, and it skips targets it cannot parse. It only bridges the registration window and
// must never become a reason for a release to fail on its own; a service
// that is really down fails the release command with its own error.
package netwait

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	defaultTimeout = 60 * time.Second
	attemptTimeout = 2 * time.Second
	retryInterval  = 500 * time.Millisecond
)

// Run parses the command line and waits for its targets.
func Run(args []string) error {
	flags := flag.NewFlagSet("net-wait", flag.ContinueOnError)
	timeout := flags.Duration("timeout", defaultTimeout, "how long to wait before giving up (and continuing anyway)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	addresses := make([]string, 0, flags.NArg())
	for _, target := range flags.Args() {
		address, err := Address(target)
		if err != nil {
			// An output that is not there leaves an unexpanded or empty
			// target; skipping it keeps the wait failing open.
			slog.Warn("net-wait: skipping target", "error", err)
			continue
		}
		addresses = append(addresses, address)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if unreachable := Wait(ctx, addresses); len(unreachable) > 0 {
		slog.Warn("net-wait: continuing with unreachable targets", "timeout", *timeout, "targets", unreachable)
	}
	return nil
}

// Address normalizes one target to host:port. A target is either host:port
// or a URL whose port defaults by scheme.
func Address(target string) (string, error) {
	if strings.Contains(target, "$(") {
		// Kubernetes leaves a reference to an unset variable (an optional
		// output key that is absent) unexpanded.
		return "", fmt.Errorf("net-wait: target %q references an unset variable", target)
	}
	if strings.Contains(target, "://") {
		parsed, err := url.Parse(target)
		if err != nil {
			return "", fmt.Errorf("net-wait: target %q: %w", target, err)
		}
		if parsed.Hostname() == "" {
			return "", fmt.Errorf("net-wait: target %q has no host", target)
		}
		port := parsed.Port()
		if port == "" {
			switch parsed.Scheme {
			case "http":
				port = "80"
			case "https":
				port = "443"
			case "postgres", "postgresql":
				port = "5432"
			default:
				return "", fmt.Errorf("net-wait: target %q has no port", target)
			}
		}
		return net.JoinHostPort(parsed.Hostname(), port), nil
	}
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return "", fmt.Errorf("net-wait: target %q: %w", target, err)
	}
	if host == "" || port == "" {
		return "", fmt.Errorf("net-wait: target %q needs a host and a port", target)
	}
	return target, nil
}

// Wait dials every address until it connects or ctx ends, and returns the
// addresses that never connected, in their given order.
func Wait(ctx context.Context, addresses []string) []string {
	reached := make([]bool, len(addresses))
	var wg sync.WaitGroup
	for index, address := range addresses {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reached[index] = waitOne(ctx, address)
		}()
	}
	wg.Wait()
	var unreachable []string
	for index, address := range addresses {
		if !reached[index] {
			unreachable = append(unreachable, address)
		}
	}
	return unreachable
}

func waitOne(ctx context.Context, address string) bool {
	dialer := net.Dialer{Timeout: attemptTimeout}
	started := time.Now()
	for attempt := 1; ; attempt++ {
		conn, err := dialer.DialContext(ctx, "tcp", address)
		if err == nil {
			_ = conn.Close()
			slog.Info("net-wait: reachable", "target", address, "attempts", attempt,
				"after", time.Since(started).Round(time.Millisecond))
			return true
		}
		select {
		case <-ctx.Done():
			slog.Warn("net-wait: unreachable", "target", address, "attempts", attempt, "error", err)
			return false
		case <-time.After(retryInterval):
		}
	}
}
