package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The ports of the remote-access scenario: where expose listens on this
// machine's network address (the remote web ports), and where the proxy
// listens on loopback (the laptop's edge ports). Both differ from the e2e
// platform's own edge ports and from a real local platform's.
const (
	e2eDoorHTTPSPort  = 9443
	e2eDoorHTTPPort   = 9082
	e2eProxyHTTPSPort = 18443
	e2eProxyHTTPPort  = 18082
)

var proxyCommandPattern = regexp.MustCompile(`remote +skali dev proxy (\S+) --ca (sha256:[0-9a-f]{64})`)

// TestDevRemoteAccess drives remote development on one machine. The
// throwaway platform plays the remote workstation: skali dev status prints
// the proxy command with its CA fingerprint, and skali dev expose opens the
// edge on a network address of this machine. A second skali, with its own
// state and loopback ports as if it ran on a laptop without a platform,
// refuses a wrong fingerprint, then proxies a browser's https and http
// requests through expose to the edge, verified against the CA it pinned.
func TestDevRemoteAccess(t *testing.T) {
	h := newE2EHarness(t)
	address := externalAddress(t)
	h.run(false, "", "dev", "-d", "--skalid-image", "skalid:dev")
	h.waitRoute("hello from skali", 5*time.Minute)

	doorEnv := []string{
		fmt.Sprintf("SKALI_DEV_REMOTE_HTTPS_PORT=%d", e2eDoorHTTPSPort),
		fmt.Sprintf("SKALI_DEV_REMOTE_HTTP_PORT=%d", e2eDoorHTTPPort),
	}
	workstation := append(append([]string(nil), h.env...), doorEnv...)
	laptop := append(append([]string(nil), os.Environ()...), append(doorEnv,
		"XDG_STATE_HOME="+t.TempDir(),
		"XDG_CONFIG_HOME="+t.TempDir(),
		// No platform here: the name must not match a real one either.
		"SKALI_DEV_CLUSTER=skali-dev-e2e-laptop",
		fmt.Sprintf("SKALI_DEV_HTTPS_PORT=%d", e2eProxyHTTPSPort),
		fmt.Sprintf("SKALI_DEV_HTTP_PORT=%d", e2eProxyHTTPPort),
	)...)
	laptopDir := t.TempDir()

	// The workstation names the command, fingerprint included.
	status := h.run(false, "", "dev", "status")
	match := proxyCommandPattern.FindStringSubmatch(status)
	require.NotNil(t, match, "dev status printed no proxy command:\n%s", status)
	require.Equal(t, "<host>", match[1])
	fingerprint := match[2]

	out := runSkali(t, h.binary, h.projectDir, workstation, false, "dev", "expose", address, "-d")
	t.Cleanup(func() { stopQuietly(h.binary, h.projectDir, workstation, "expose") })
	require.Contains(t, out, "in the background")
	require.Contains(t, out, "skali dev proxy "+address+" --ca "+fingerprint)
	status = runSkali(t, h.binary, h.projectDir, workstation, false, "dev", "status")
	require.Contains(t, status, "local platform on "+address)
	require.Contains(t, status, "skali dev proxy "+address+" --ca "+fingerprint)

	// The laptop: a wrong fingerprint is refused before anything is served.
	out = runSkali(t, h.binary, laptopDir, laptop, true, "dev", "proxy", address, "--ca", "sha256:"+strings.Repeat("ab", 32))
	require.Contains(t, out, "does not match the fingerprint")
	require.NotContains(t, out, fingerprint)

	out = runSkali(t, h.binary, laptopDir, laptop, false, "dev", "proxy", address, "--ca", fingerprint, "-d")
	t.Cleanup(func() { stopQuietly(h.binary, laptopDir, laptop, "proxy") })
	require.Contains(t, out, "Proxy running in the background")

	// The pinned CA is the platform's own, and verifies the route served
	// through proxy, expose, and the edge, under the route's own name.
	stateHome := envValue(laptop, "XDG_STATE_HOME")
	pinned, err := os.ReadFile(filepath.Join(stateHome, "skali", "remote-dev", "ca.crt"))
	require.NoError(t, err)
	local, err := os.ReadFile(filepath.Join(h.stateDir(), "skali", "dev", e2eCluster, "ca.crt"))
	require.NoError(t, err)
	require.Equal(t, strings.TrimSpace(string(local)), strings.TrimSpace(string(pinned)))
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(pinned))

	get := func(scheme, host string, port int, path string) (int, http.Header, string) {
		t.Helper()
		request, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s://127.0.0.1:%d%s", scheme, port, path), nil)
		require.NoError(t, err)
		request.Host = host
		client := &http.Client{
			Timeout:       10 * time.Second,
			Transport:     &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: host}, DisableKeepAlives: true},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
		response, err := client.Do(request)
		require.NoError(t, err)
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		return response.StatusCode, response.Header, string(body)
	}
	code, _, body := get("https", h.host, e2eProxyHTTPSPort, "/")
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, "hello from skali")
	code, _, body = get("https", "skali.localhost", e2eProxyHTTPSPort, "/api/healthz")
	require.Equal(t, http.StatusOK, code, body)
	code, header, _ := get("http", h.host, e2eProxyHTTPPort, "/")
	require.Contains(t, []int{http.StatusMovedPermanently, http.StatusPermanentRedirect}, code)
	require.True(t, strings.HasPrefix(header.Get("Location"), "https://"+h.host), header.Get("Location"))

	// The laptop reports the proxy without a platform of its own, and
	// refuses to create one while the proxy holds the edge ports.
	status = runSkali(t, h.binary, laptopDir, laptop, false, "dev", "status")
	require.Contains(t, status, "forwarding *.localhost to "+address)
	out = runSkali(t, h.binary, laptopDir, laptop, true, "dev", "start")
	require.Contains(t, out, "stop it with skali dev proxy stop first")

	require.Contains(t, runSkali(t, h.binary, laptopDir, laptop, false, "dev", "proxy", "stop"), "stopped skali dev proxy")
	require.Contains(t, runSkali(t, h.binary, h.projectDir, workstation, false, "dev", "expose", "stop"), "stopped skali dev expose")
	_, err = net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", e2eProxyHTTPSPort), time.Second)
	require.Error(t, err, "the proxy released its port")

	h.run(false, "", "dev", "down")
}

// runSkali runs the built CLI with an explicit environment and returns its
// combined output; fatal on an unexpected exit status.
func runSkali(t *testing.T, binary, dir string, env []string, wantErr bool, args ...string) string {
	t.Helper()
	command := exec.Command(binary, args...)
	command.Dir = dir
	command.Env = env
	out, err := command.CombinedOutput()
	if wantErr {
		require.Error(t, err, "skali %s should fail:\n%s", strings.Join(args, " "), out)
	} else {
		require.NoError(t, err, "skali %s failed:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

// stopQuietly ends a background session left running by a failed run;
// after a passing run it is already stopped.
func stopQuietly(binary, dir string, env []string, session string) {
	command := exec.Command(binary, "dev", session, "stop")
	command.Dir = dir
	command.Env = env
	_ = command.Run()
}

func envValue(env []string, name string) string {
	value := ""
	for _, entry := range env {
		if found, ok := strings.CutPrefix(entry, name+"="); ok {
			value = found
		}
	}
	return value
}

// externalAddress is an IPv4 address of this machine other than loopback,
// where expose can listen; the scenario needs one.
func externalAddress(t *testing.T) string {
	t.Helper()
	interfaces, err := net.Interfaces()
	require.NoError(t, err)
	for _, candidate := range interfaces {
		if candidate.Flags&net.FlagUp == 0 || candidate.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := candidate.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			prefix, err := netip.ParsePrefix(address.String())
			if err != nil {
				continue
			}
			if addr := prefix.Addr(); addr.Is4() && !addr.IsLoopback() && !addr.IsLinkLocalUnicast() {
				return addr.String()
			}
		}
	}
	t.Skip("no non-loopback IPv4 address for skali dev expose to listen on")
	return ""
}
