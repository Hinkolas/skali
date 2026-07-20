package kubetest

import (
	"io"
	"net"
	"net/url"
	"sync"
	"testing"

	"k8s.io/client-go/rest"
)

// Proxy is a severable TCP forwarder between a kube client and the real API
// server, so watch-disconnect tests can cut and restore connectivity
// without touching the cluster.
type Proxy struct {
	listener net.Listener
	target   string

	mu        sync.Mutex
	accepting bool
	conns     map[net.Conn]struct{}
}

// NewProxy forwards local connections to target (host:port) until severed.
func NewProxy(t *testing.T, target string) *Proxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("start proxy listener: %v", err)
	}
	p := &Proxy{
		listener:  listener,
		target:    target,
		accepting: true,
		conns:     make(map[net.Conn]struct{}),
	}
	go p.acceptLoop()
	t.Cleanup(p.Close)
	return p
}

func (p *Proxy) Addr() string { return p.listener.Addr().String() }

// Sever kills every open connection and refuses new ones until Resume.
func (p *Proxy) Sever() {
	p.mu.Lock()
	p.accepting = false
	for conn := range p.conns {
		_ = conn.Close()
		delete(p.conns, conn)
	}
	p.mu.Unlock()
}

// Resume restores forwarding for new connections.
func (p *Proxy) Resume() {
	p.mu.Lock()
	p.accepting = true
	p.mu.Unlock()
}

func (p *Proxy) Close() {
	p.Sever()
	_ = p.listener.Close()
}

func (p *Proxy) acceptLoop() {
	for {
		client, err := p.listener.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		accepting := p.accepting
		if accepting {
			p.conns[client] = struct{}{}
		}
		p.mu.Unlock()
		if !accepting {
			_ = client.Close()
			continue
		}
		go p.forward(client)
	}
}

func (p *Proxy) forward(client net.Conn) {
	upstream, err := net.Dial("tcp", p.target)
	if err != nil {
		_ = client.Close()
		p.forget(client)
		return
	}
	p.mu.Lock()
	if !p.accepting {
		p.mu.Unlock()
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	p.conns[upstream] = struct{}{}
	p.mu.Unlock()

	done := make(chan struct{}, 2)
	pipe := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		done <- struct{}{}
	}
	go pipe(upstream, client)
	go pipe(client, upstream)
	<-done
	_ = client.Close()
	_ = upstream.Close()
	p.forget(client)
	p.forget(upstream)
}

func (p *Proxy) forget(conn net.Conn) {
	p.mu.Lock()
	delete(p.conns, conn)
	p.mu.Unlock()
}

// ProxiedConfig routes the test cluster's rest.Config through a severable
// proxy: the host points at the proxy while TLS keeps verifying against the
// original server name.
func ProxiedConfig(t *testing.T) (*rest.Config, *Proxy) {
	t.Helper()
	config := Config(t)
	parsed, err := url.Parse(config.Host)
	if err != nil {
		t.Fatalf("parse API server host %s: %v", config.Host, err)
	}
	proxy := NewProxy(t, parsed.Host)
	if config.TLSClientConfig.ServerName == "" {
		config.TLSClientConfig.ServerName = parsed.Hostname()
	}
	config.Host = parsed.Scheme + "://" + proxy.Addr()
	return config, proxy
}
