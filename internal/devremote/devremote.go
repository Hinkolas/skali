// Package devremote reaches a local skali platform that runs on another
// machine: the developer's checkout, cluster, and agent sessions live on a
// remote workstation while the browser that has to look at a route runs on
// a laptop.
//
// The laptop keeps the routes' https://<domain>.localhost origins. A proxy
// listens on the laptop's loopback edge ports and relays raw TCP to the
// remote edge, so TLS, SNI, Host, Origin, and cookies arrive exactly as
// they would locally and nothing is rewritten. The remote edge only
// listens on its own loopback, so some door has to pass outside traffic to
// it: Tailscale Serve, an SSH forward, or expose, which listens on an
// address of the remote machine the developer chose. How the laptop
// reaches that door (a LAN, a VPN, a tailnet) is not this package's
// concern.
//
// Browsers on the laptop must trust the remote installation's development
// CA. It is fetched through the same transport and accepted only when it
// matches a fingerprint the developer copied from the remote machine:
// a trusted root without name constraints could otherwise sign any site.
package devremote

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Hinkolas/skali/internal/localdev"
)

// RemoteHTTPSPort and RemoteHTTPPort are the ports the proxy connects to
// and expose listens on: the web ports, so every setup reads the same.
// The overrides exist for the end-to-end suite, which runs both ends on
// one machine next to a local platform on other ports.
func RemoteHTTPSPort() int { return envPortOr("SKALI_DEV_REMOTE_HTTPS_PORT", 443) }
func RemoteHTTPPort() int  { return envPortOr("SKALI_DEV_REMOTE_HTTP_PORT", 80) }

// Dir holds this machine's remote development state: the pinned CA of the
// remote installation and the records, locks, and logs of the proxy and
// expose processes.
func Dir() (string, error) {
	state, err := localdev.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(state, "remote-dev"), nil
}

// PinnedCAPath is where a remote installation's CA certificate is kept
// once it matched fingerprint; the trust stores import it from there. Each
// fingerprint has a file of its own, so a concurrent run pinning another
// CA can never swap the file between verification and installation.
func PinnedCAPath(fingerprint string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ca-"+strings.TrimPrefix(fingerprint, fingerprintPrefix)+".crt"), nil
}

// hostPort joins a host and a port into a dialable address.
func hostPort(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func envPortOr(name string, fallback int) int {
	if value := os.Getenv(name); value != "" {
		if port, err := strconv.Atoi(value); err == nil && port > 0 && port < 65536 {
			return port
		}
	}
	return fallback
}
