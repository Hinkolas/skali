package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/devremote"
)

// remoteEdge is a stand-in for another machine's skali dev edge: a TLS
// server whose certificate a development CA issued for the platform host
// and every route host, serving that CA where skalid does, plus the plain
// HTTP entrypoint.
type remoteEdge struct {
	ca          *x509.Certificate
	caPEM       []byte
	fingerprint string
	httpsPort   int
	httpPort    int
}

func startRemoteEdge(t *testing.T) *remoteEdge {
	t.Helper()
	return startGatedRemoteEdge(t, nil)
}

// startGatedRemoteEdge holds every CA request until gate closes, to catch
// the proxy in the middle of its startup.
func startGatedRemoteEdge(t *testing.T, gate <-chan struct{}) *remoteEdge {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "skali local dev CA (remote, test)"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true, IsCA: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	ca, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	leafDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "skali.localhost"},
		DNSNames:  []string{"skali.localhost", "app.localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}, ca, &leafKey.PublicKey, caKey)
	require.NoError(t, err)

	edge := &remoteEdge{ca: ca, caPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		fingerprint: devremote.Fingerprint(caDER)}
	secure := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "skali.localhost" && r.URL.Path == "/api/dev/ca.crt" {
			if gate != nil {
				<-gate
			}
			_, _ = w.Write(edge.caPEM)
			return
		}
		fmt.Fprintf(w, "remote edge: %s%s over %s", r.Host, r.URL.Path, r.TLS.ServerName)
	}))
	secure.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}}}
	secure.StartTLS()
	t.Cleanup(secure.Close)
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://"+r.Host+r.URL.Path, http.StatusPermanentRedirect)
	}))
	t.Cleanup(plain.Close)
	edge.httpsPort = listenerPort(t, secure.Listener.Addr())
	edge.httpPort = listenerPort(t, plain.Listener.Addr())
	return edge
}

func listenerPort(t *testing.T, addr net.Addr) int {
	t.Helper()
	_, port, err := net.SplitHostPort(addr.String())
	require.NoError(t, err)
	value, err := strconv.Atoi(port)
	require.NoError(t, err)
	return value
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	return listenerPort(t, listener.Addr())
}

// remoteDevEnv isolates this machine's state and points the proxy's
// loopback ports and the remote ports at the test's own.
func remoteDevEnv(t *testing.T, edge *remoteEdge) (httpsPort, httpPort int) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	httpsPort, httpPort = freePort(t), freePort(t)
	t.Setenv("SKALI_DEV_HTTPS_PORT", strconv.Itoa(httpsPort))
	t.Setenv("SKALI_DEV_HTTP_PORT", strconv.Itoa(httpPort))
	if edge != nil {
		t.Setenv("SKALI_DEV_REMOTE_HTTPS_PORT", strconv.Itoa(edge.httpsPort))
		t.Setenv("SKALI_DEV_REMOTE_HTTP_PORT", strconv.Itoa(edge.httpPort))
	}
	return httpsPort, httpPort
}

// The whole foreground path in one process: the CA is fetched and pinned,
// the target remembered, both ports relay to the remote edge with the
// browser's own TLS and Host, and the local platform refuses to start
// while the proxy holds the ports.
func TestDevProxyRelaysToTheRemoteEdge(t *testing.T) {
	edge := startRemoteEdge(t)
	httpsPort, httpPort := remoteDevEnv(t, edge)

	command := newDevProxyCommand()
	output := &syncBuffer{}
	command.SetOut(output)
	command.SetErr(output)
	command.SetArgs([]string{"127.0.0.1", "--ca", strings.ToUpper(edge.fingerprint)})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- command.ExecuteContext(ctx) }()
	require.Eventually(t, func() bool { return strings.Contains(output.String(), "Forwarding *.localhost") },
		10*time.Second, 20*time.Millisecond, "the proxy never came up:\n%s", output.String())
	require.Contains(t, output.String(), "skali local dev CA (remote, test)")

	roots := x509.NewCertPool()
	roots.AddCert(edge.ca)
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "app.localhost"},
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				_, port, _ := net.SplitHostPort(address)
				return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Get(fmt.Sprintf("https://app.localhost:%d/page", httpsPort))
	require.NoError(t, err)
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	require.Equal(t, fmt.Sprintf("remote edge: app.localhost:%d/page over app.localhost", httpsPort), string(body))

	response, err = client.Get(fmt.Sprintf("http://app.localhost:%d/page", httpPort))
	require.NoError(t, err)
	response.Body.Close()
	require.Equal(t, http.StatusPermanentRedirect, response.StatusCode)

	// A second proxy and the local platform both refuse while it runs.
	second := newDevProxyCommand()
	second.SetOut(io.Discard)
	second.SetErr(io.Discard)
	second.SetArgs([]string{"127.0.0.1", "--ca", edge.fingerprint})
	require.ErrorContains(t, second.Execute(), "already running (127.0.0.1, pid")
	require.ErrorContains(t, refuseWhileProxying(), "forwards this machine's *.localhost to 127.0.0.1")

	var status bytes.Buffer
	printRemoteDevSessions(&status, clirender.StyleFor(&status))
	require.Contains(t, status.String(), "forwarding *.localhost to 127.0.0.1")
	require.Contains(t, status.String(), edge.fingerprint[:len("sha256:")+12]+"…")

	cancel()
	require.NoError(t, <-done)
	require.Contains(t, output.String(), "proxy stopped")
	require.NoError(t, refuseWhileProxying())

	session, err := devremote.ProxySession()
	require.NoError(t, err)
	host, fingerprint, err := proxyTarget(session, nil, "")
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", host, "a bare skali dev proxy reuses the host")
	require.Equal(t, edge.fingerprint, fingerprint, "and the fingerprint, normalized")
	pinned, err := devremote.PinnedCAPath(edge.fingerprint)
	require.NoError(t, err)
	require.FileExists(t, pinned)
}

// A wrong fingerprint stops the proxy before it serves anything, and the
// error does not repeat the fingerprint that was seen.
func TestDevProxyRefusesAnotherCA(t *testing.T) {
	edge := startRemoteEdge(t)
	remoteDevEnv(t, edge)
	command := newDevProxyCommand()
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs([]string{"127.0.0.1", "--ca", "sha256:" + strings.Repeat("ab", 32)})
	err := command.Execute()
	require.ErrorIs(t, err, devremote.ErrFingerprintMismatch)
	require.NotContains(t, err.Error(), edge.fingerprint[len("sha256:"):])

	session, err := devremote.ProxySession()
	require.NoError(t, err)
	record, err := session.Load()
	require.NoError(t, err)
	require.Empty(t, record.Host, "nothing is remembered from a refused run")
	require.Empty(t, record.Fingerprint)
}

func TestProxyTargetNeedsHostAndFingerprintOnce(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	session, err := devremote.ProxySession()
	require.NoError(t, err)
	_, _, err = proxyTarget(session, nil, "")
	require.ErrorContains(t, err, "pass the host")
	_, _, err = proxyTarget(session, []string{"mac-mini"}, "")
	require.ErrorContains(t, err, "--ca is required the first time")
	_, _, err = proxyTarget(session, []string{"mac-mini:8443"}, "sha256:"+strings.Repeat("ab", 32))
	require.ErrorContains(t, err, "carries a port")

	require.NoError(t, session.Save(devremote.Record{Host: "mac-mini", Fingerprint: "sha256:" + strings.Repeat("ab", 32)}))
	host, fingerprint, err := proxyTarget(session, []string{"192.168.69.100"}, "")
	require.NoError(t, err)
	require.Equal(t, "192.168.69.100", host, "a new host keeps the pinned fingerprint")
	require.Equal(t, "sha256:"+strings.Repeat("ab", 32), fingerprint)
	_, fingerprint, err = proxyTarget(session, nil, "SHA256:"+strings.Repeat("CD", 32))
	require.NoError(t, err)
	require.Equal(t, "sha256:"+strings.Repeat("cd", 32), fingerprint, "a new fingerprint replaces the pinned one")
}

func TestDevTrustHostForm(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	run := func(args ...string) error {
		command := newDevTrustCommand()
		command.SetOut(io.Discard)
		command.SetErr(io.Discard)
		command.SetArgs(args)
		return command.Execute()
	}
	require.ErrorContains(t, run("--ca", "sha256:"+strings.Repeat("ab", 32)), "pass that machine's host too")
	require.ErrorContains(t, run("mac-mini"), "--ca is required with a host")
	require.ErrorContains(t, run("mac-mini", "--ca", "nope"), "SHA-256 digest")
	require.ErrorContains(t, run("--check"), "or pass a host to trust a platform on another machine")
}

func TestExplainProxyBind(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	route := devremote.Route{Scheme: "https", Listen: "127.0.0.1:443"}
	inUse := &devremote.BindError{Route: route, Err: &net.OpError{Op: "listen", Err: syscall.EADDRINUSE}}
	require.ErrorContains(t, explainProxyBind(inUse), "port 443 is in use on this machine")
	require.ErrorContains(t, explainProxyBind(inUse), "lsof -nP -iTCP:443")
	denied := &devremote.BindError{Route: route, Err: &net.OpError{Op: "listen", Err: syscall.EACCES}}
	require.ErrorContains(t, explainProxyBind(denied), "net.ipv4.ip_unprivileged_port_start=80")
}

// An address this machine does not have is named as such; 192.0.2.1 is
// reserved for documentation and never assigned.
func TestDevExposeExplainsForeignAddress(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("SKALI_DEV_REMOTE_HTTPS_PORT", strconv.Itoa(freePort(t)))
	t.Setenv("SKALI_DEV_REMOTE_HTTP_PORT", strconv.Itoa(freePort(t)))
	session, err := devremote.ExposeSession()
	require.NoError(t, err)
	addr, err := exposeAddress(session, []string{"192.0.2.1"})
	require.NoError(t, err)
	_, err = devremote.Listen(devremote.ExposeRoutes(addr))
	require.Error(t, err)
	require.ErrorContains(t, explainExposeBind(err, addr), "192.0.2.1 is not an address of this machine")

	_, err = exposeAddress(session, nil)
	require.ErrorContains(t, err, "pass the address of this machine")
	require.NoError(t, session.Save(devremote.Record{Host: "192.0.2.1"}))
	addr, err = exposeAddress(session, nil)
	require.NoError(t, err)
	require.Equal(t, "192.0.2.1", addr.String(), "a bare skali dev expose reuses the address")
}

func TestDevExposeRequiresALocalPlatform(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	command := newDevExposeCommand()
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs([]string{"192.168.69.100"})
	require.ErrorContains(t, command.Execute(), "no local platform on this machine to expose")
}

func TestProxyCommandLine(t *testing.T) {
	t.Parallel()
	require.Equal(t, "skali dev proxy 192.168.69.100 --ca sha256:ab", proxyCommandLine("192.168.69.100", "sha256:ab"))
	require.Equal(t, "skali dev proxy fd7a::1 --ca sha256:ab", proxyCommandLine("fd7a::1", "sha256:ab"),
		"zsh would glob a bracketed address")
}

func TestStopWithoutASession(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	session, err := devremote.ProxySession()
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, stopDevSession(&out, session))
	require.Equal(t, "skali dev proxy is not running\n", out.String())
}

// A laptop that only runs the proxy has no platform, and Docker (which
// k3d needs) may not run there at all: status says so instead of failing.
func TestDevStatusWithoutPlatformOrDocker(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	root := newRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"dev", "status"})
	require.NoError(t, root.Execute())
	require.Equal(t, "platform   not installed\n", out.String())
}

// A loopback target on the proxy's own ports would relay into itself; the
// end-to-end setups move the remote ports and stay allowed.
func TestDevProxyRefusesToForwardToItself(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, host := range []string{"localhost", "127.0.0.1", "::1", "skali.localhost"} {
		require.True(t, forwardsToItself(host), host)
	}
	require.False(t, forwardsToItself("mac-mini"))
	require.False(t, forwardsToItself("192.168.69.100"))

	command := newDevProxyCommand()
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs([]string{"localhost", "--ca", "sha256:" + strings.Repeat("ab", 32)})
	require.ErrorContains(t, command.Execute(), "skali dev trust localhost --ca sha256:")

	t.Setenv("SKALI_DEV_REMOTE_HTTPS_PORT", "18443")
	t.Setenv("SKALI_DEV_REMOTE_HTTP_PORT", "18080")
	require.False(t, forwardsToItself("127.0.0.1"), "an SSH forward on other ports is a valid target")
}

// While a starting proxy still fetches the CA, the session already names
// this process, not the one a previous run recorded: stop would otherwise
// signal whatever process now has that old id.
func TestDevProxyPublishesItsOwnerBeforeTheCAFetch(t *testing.T) {
	gate := make(chan struct{})
	edge := startGatedRemoteEdge(t, gate)
	remoteDevEnv(t, edge)
	session, err := devremote.ProxySession()
	require.NoError(t, err)
	require.NoError(t, session.Save(devremote.Record{Host: "127.0.0.1", Fingerprint: edge.fingerprint, PID: 999999}))

	command := newDevProxyCommand()
	output := &syncBuffer{}
	command.SetOut(output)
	command.SetErr(output)
	command.SetArgs([]string{"127.0.0.1"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- command.ExecuteContext(ctx) }()

	require.Eventually(t, func() bool {
		_, running, err := session.Running()
		return err == nil && running
	}, 5*time.Second, 10*time.Millisecond)
	record, running, err := session.Running()
	require.NoError(t, err)
	require.True(t, running)
	require.Equal(t, os.Getpid(), record.PID, "the stale id of the previous run is gone")

	close(gate)
	require.Eventually(t, func() bool { return strings.Contains(output.String(), "Forwarding *.localhost") },
		10*time.Second, 20*time.Millisecond, "the proxy never came up:\n%s", output.String())
	cancel()
	require.NoError(t, <-done)
}
