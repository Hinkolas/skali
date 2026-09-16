package edgeprobe

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/edge"
)

const testInstance = "11111111-2222-4333-8444-555555555555"

// fakeResolver answers from a fixed table; unknown names are NXDOMAIN.
type fakeResolver map[string][]net.IPAddr

func (r fakeResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	addresses, ok := r[host]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return addresses, nil
}

type erroringResolver struct{}

func (erroringResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return nil, &net.DNSError{Err: "server misbehaving", IsTemporary: true}
}

// recordingServer is one probed address: it records every request and
// answers with a fixed status, optionally as this installation.
type recordingServer struct {
	server *httptest.Server
	mu     sync.Mutex
	hosts  []string
	agents []string
}

func newServer(t *testing.T, ours bool, status int, delay time.Duration) *recordingServer {
	t.Helper()
	rec := &recordingServer{}
	rec.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.hosts = append(rec.hosts, r.Host)
		rec.agents = append(rec.agents, r.UserAgent())
		rec.mu.Unlock()
		time.Sleep(delay)
		if ours {
			w.Header().Set(edge.InstanceHeader, testInstance)
		}
		if status >= 300 && status < 400 {
			w.Header().Set("Location", "https://example.com"+r.URL.Path)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(rec.server.Close)
	return rec
}

func (s *recordingServer) requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.hosts)
}

func ip(s string) net.IPAddr { return net.IPAddr{IP: net.ParseIP(s)} }

// prober maps synthetic addresses onto local servers (or a closed port).
func prober(resolver Resolver, targets map[string]string) *Prober {
	p := New(testInstance, "test")
	p.Resolver = resolver
	p.PerAddressTimeout = 500 * time.Millisecond
	p.Budget = 2 * time.Second
	p.target = func(address net.IP) string {
		if target, ok := targets[address.String()]; ok {
			return target
		}
		return net.JoinHostPort(address.String(), "80")
	}
	return p
}

func hostPort(s *recordingServer) string { return strings.TrimPrefix(s.server.URL, "http://") }

// closedPort returns a loopback host:port nothing listens on.
func closedPort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	return address
}

func TestProbeReachable(t *testing.T) {
	t.Parallel()
	ours := newServer(t, true, http.StatusOK, 0)
	p := prober(fakeResolver{"shop.example.com": {ip("192.0.2.1")}}, map[string]string{"192.0.2.1": hostPort(ours)})

	result := p.Probe(context.Background(), "shop.example.com")

	require.Equal(t, StateReachable, result.State)
	require.False(t, result.State.Pending())
	require.Equal(t, "shop.example.com", result.Domain)
	require.Len(t, result.Addresses, 1)
	require.Equal(t, AddressResult{Address: "192.0.2.1", Outcome: OutcomeOurs, Detail: "HTTP 200"}, result.Addresses[0])
	require.Equal(t, "the address answers as this installation", result.Message)
	require.WithinDuration(t, time.Now(), result.CheckedAt, 5*time.Second)
	// The request carried the domain, not the address, and identified itself.
	require.Equal(t, []string{"shop.example.com"}, ours.hosts)
	require.Equal(t, []string{"skali-edge-probe/test"}, ours.agents)
}

func TestProbeForeignRedirectIsNotFollowed(t *testing.T) {
	t.Parallel()
	old := newServer(t, false, http.StatusMovedPermanently, 0)
	p := prober(fakeResolver{"shop.example.com": {ip("192.0.2.1")}}, map[string]string{"192.0.2.1": hostPort(old)})

	result := p.Probe(context.Background(), "shop.example.com")

	require.Equal(t, StateUnreachable, result.State)
	require.True(t, result.State.Pending())
	require.Equal(t, OutcomeForeign, result.Addresses[0].Outcome)
	require.Equal(t, "HTTP 301 without Skali-Instance", result.Addresses[0].Detail)
	require.Equal(t, "no address answers as this installation; 192.0.2.1 answers as another server", result.Message)
	require.Equal(t, 1, old.requests(), "a redirect from the old host is an answer, not a hint to follow")
}

func TestProbeForeignNotFound(t *testing.T) {
	t.Parallel()
	old := newServer(t, false, http.StatusNotFound, 0)
	p := prober(fakeResolver{"shop.example.com": {ip("192.0.2.1")}}, map[string]string{"192.0.2.1": hostPort(old)})

	result := p.Probe(context.Background(), "shop.example.com")

	require.Equal(t, StateUnreachable, result.State)
	require.Equal(t, OutcomeForeign, result.Addresses[0].Outcome)
}

func TestProbePartial(t *testing.T) {
	t.Parallel()
	ours := newServer(t, true, http.StatusOK, 0)
	old := newServer(t, false, http.StatusOK, 0)
	p := prober(fakeResolver{"shop.example.com": {ip("192.0.2.1"), ip("2001:db8::1")}},
		map[string]string{"192.0.2.1": hostPort(ours), "2001:db8::1": hostPort(old)})

	result := p.Probe(context.Background(), "shop.example.com")

	require.Equal(t, StatePartial, result.State)
	require.True(t, result.State.Pending())
	require.Equal(t, OutcomeOurs, result.Addresses[0].Outcome)
	require.Equal(t, OutcomeForeign, result.Addresses[1].Outcome)
	require.Equal(t, "1 of 2 addresses answer as this installation; 2001:db8::1 answers as another server", result.Message)
}

func TestProbeClosedPortIsUnreachable(t *testing.T) {
	t.Parallel()
	p := prober(fakeResolver{"shop.example.com": {ip("192.0.2.1")}}, map[string]string{"192.0.2.1": closedPort(t)})

	result := p.Probe(context.Background(), "shop.example.com")

	require.Equal(t, StateUnreachable, result.State)
	require.Equal(t, OutcomeUnreachable, result.Addresses[0].Outcome)
	require.NotEmpty(t, result.Addresses[0].Detail)
	require.Equal(t, "the address did not answer at all", result.Message)
}

func TestProbeOursPlusUnreachableIsReachable(t *testing.T) {
	t.Parallel()
	ours := newServer(t, true, http.StatusOK, 0)
	p := prober(fakeResolver{"shop.example.com": {ip("192.0.2.1"), ip("2001:db8::1")}},
		map[string]string{"192.0.2.1": hostPort(ours), "2001:db8::1": closedPort(t)})

	result := p.Probe(context.Background(), "shop.example.com")

	// An address that never answers is inconclusive (an IPv6 record probed
	// from an IPv4-only pod looks exactly like this); it never outvotes an
	// address that answered as this installation.
	require.Equal(t, StateReachable, result.State)
	require.Equal(t, "1 of 2 addresses answer as this installation; the rest did not answer", result.Message)
}

func TestProbeUnresolved(t *testing.T) {
	t.Parallel()
	p := prober(fakeResolver{}, nil)

	result := p.Probe(context.Background(), "gone.example.com")

	require.Equal(t, StateUnresolved, result.State)
	require.True(t, result.State.Pending())
	require.Empty(t, result.Addresses)
	require.Equal(t, "the domain does not resolve", result.Message)
}

func TestProbeResolverErrorIsUnknown(t *testing.T) {
	t.Parallel()
	p := prober(erroringResolver{}, nil)

	result := p.Probe(context.Background(), "shop.example.com")

	require.Equal(t, StateUnknown, result.State)
	require.False(t, result.State.Pending())
	require.Contains(t, result.Message, "probe failed: ")
}

func TestProbeCancelledContextIsUnknown(t *testing.T) {
	t.Parallel()
	slow := newServer(t, true, http.StatusOK, 300*time.Millisecond)
	p := prober(fakeResolver{"shop.example.com": {ip("192.0.2.1")}}, map[string]string{"192.0.2.1": hostPort(slow)})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	result := p.Probe(ctx, "shop.example.com")

	require.Equal(t, StateUnknown, result.State)
	require.True(t, errors.Is(ctx.Err(), context.Canceled))
}

func TestProbeCapsAddresses(t *testing.T) {
	t.Parallel()
	ours := newServer(t, true, http.StatusOK, 0)
	var addresses []net.IPAddr
	targets := map[string]string{}
	for i := 1; i <= 12; i++ {
		address := net.IPv4(192, 0, 2, byte(i))
		addresses = append(addresses, net.IPAddr{IP: address})
		targets[address.String()] = hostPort(ours)
	}
	p := prober(fakeResolver{"shop.example.com": addresses}, targets)

	result := p.Probe(context.Background(), "shop.example.com")

	require.Len(t, result.Addresses, 8)
	require.Equal(t, "192.0.2.1", result.Addresses[0].Address, "resolver order is kept")
	require.Equal(t, StateReachable, result.State)
	require.Equal(t, "all 8 addresses answer as this installation", result.Message)
}

func TestProbeSlowAddressIsBoundedByTimeout(t *testing.T) {
	t.Parallel()
	slow := newServer(t, true, http.StatusOK, 2*time.Second)
	p := prober(fakeResolver{"shop.example.com": {ip("192.0.2.1")}}, map[string]string{"192.0.2.1": hostPort(slow)})

	started := time.Now()
	result := p.Probe(context.Background(), "shop.example.com")

	require.Less(t, time.Since(started), p.Budget+time.Second)
	require.Equal(t, StateUnreachable, result.State, "the probe's own budget expiring is a verdict, not an error")
	require.Equal(t, OutcomeUnreachable, result.Addresses[0].Outcome)
	require.Equal(t, "timed out", result.Addresses[0].Detail)
}

func TestStatePending(t *testing.T) {
	t.Parallel()
	for state, pending := range map[State]bool{
		StateReachable: false, StatePartial: true, StateUnreachable: true, StateUnresolved: true, StateUnknown: false,
	} {
		require.Equal(t, pending, state.Pending(), string(state))
	}
}
