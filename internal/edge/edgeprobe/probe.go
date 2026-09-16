// Package edgeprobe answers one question for the kernel: does a route's
// public domain currently reach this installation's edge? It resolves the
// domain and requests the edge identity route (edge.ProbePath) from every
// address with the domain as the Host header, then recognises its own edge
// by the instance header. Reachability is judged by an actual request, not
// by comparing DNS records, so load balancers, anycast fronts and multi-edge
// installations all read correctly. Like internal/edge it stays free of
// skali imports beyond that package.
package edgeprobe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Hinkolas/skali/internal/edge"
)

// State is the aggregate verdict for one domain.
type State string

const (
	// StateReachable: at least one address answered as this installation
	// and none answered as another server.
	StateReachable State = "reachable"
	// StatePartial: this installation answers on some addresses while
	// another server answers on others, typically an A record moved and an
	// AAAA record left behind. Certificate authorities prefer IPv6, so this
	// counts as not arrived.
	StatePartial State = "partial"
	// StateUnreachable: the domain resolves but no address answered as
	// this installation.
	StateUnreachable State = "unreachable"
	// StateUnresolved: the domain has no address records.
	StateUnresolved State = "unresolved"
	// StateUnknown: the probe itself failed (resolver error, cancelled
	// context). Callers keep their default behaviour on it, so a broken
	// probe never hides a real issuance failure.
	StateUnknown State = "unknown"
)

// Pending reports a domain that has not arrived at this edge yet: the
// deferral condition. Unknown is not pending.
func (s State) Pending() bool {
	return s == StateUnreachable || s == StatePartial || s == StateUnresolved
}

// Address outcomes.
const (
	// OutcomeOurs: the address answered with this installation's identity.
	OutcomeOurs = "ours"
	// OutcomeForeign: the address answered HTTP without the identity, so
	// another server owns it (a redirect, a 404, a foreign 200 alike).
	OutcomeForeign = "foreign"
	// OutcomeUnreachable: no HTTP answer at all (refused, timed out, no
	// route). Inconclusive: an IPv6 address probed from an IPv4-only pod
	// reads like this too.
	OutcomeUnreachable = "unreachable"
)

// AddressResult is one address's verdict.
type AddressResult struct {
	Address string
	Outcome string
	Detail  string
}

// Result is one probe of one domain.
type Result struct {
	Domain    string
	State     State
	Addresses []AddressResult
	// Message is one human sentence summarising the verdict.
	Message   string
	CheckedAt time.Time
}

// Resolver is the DNS lookup the prober uses; net.DefaultResolver in
// production, a fake in tests.
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// Prober probes domains for one installation. The zero value is not
// usable; construct it with New.
type Prober struct {
	// InstanceID is this installation's identity, compared against the
	// instance header of every answer.
	InstanceID string
	// Resolver resolves domains; nil uses net.DefaultResolver.
	Resolver Resolver
	// Client issues the requests; nil uses a keep-alive-free client that
	// never follows redirects. A redirect from the old host is a foreign
	// answer, not a hint to go elsewhere.
	Client *http.Client
	// UserAgent identifies the probe in the old provider's access logs.
	UserAgent string
	// MaxAddresses caps how many resolved addresses are probed; zero means
	// eight. Resolver order is kept.
	MaxAddresses int
	// PerAddressTimeout bounds one request; zero means three seconds.
	PerAddressTimeout time.Duration
	// Budget bounds the whole probe; zero means five seconds.
	Budget time.Duration
	// target builds the host:port to dial for an address; nil dials port
	// 80. Tests point synthetic addresses at local servers.
	target func(net.IP) string
}

// New returns a prober for the installation identified by instanceID.
// version goes into the User-Agent.
func New(instanceID, version string) *Prober {
	return &Prober{
		InstanceID: instanceID,
		UserAgent:  "skali-edge-probe/" + version,
	}
}

const (
	defaultMaxAddresses      = 8
	defaultPerAddressTimeout = 3 * time.Second
	defaultBudget            = 5 * time.Second
)

func defaultClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: timeout}
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           dialer.DialContext,
			DisableKeepAlives:     true,
			ResponseHeaderTimeout: timeout,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Probe resolves domain and requests the edge identity from every address.
func (p *Prober) Probe(ctx context.Context, domain string) Result {
	result := Result{Domain: domain, CheckedAt: time.Now()}
	budget := p.Budget
	if budget <= 0 {
		budget = defaultBudget
	}
	perAddress := p.PerAddressTimeout
	if perAddress <= 0 {
		perAddress = defaultPerAddressTimeout
	}
	maxAddresses := p.MaxAddresses
	if maxAddresses <= 0 {
		maxAddresses = defaultMaxAddresses
	}
	resolver := p.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	client := p.Client
	if client == nil {
		client = defaultClient(perAddress)
	}
	target := p.target
	if target == nil {
		target = func(ip net.IP) string { return net.JoinHostPort(ip.String(), "80") }
	}

	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	addresses, err := resolver.LookupIPAddr(ctx, domain)
	if err != nil {
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			result.State = StateUnresolved
			result.Message = "the domain does not resolve"
			return result
		}
		result.State = StateUnknown
		result.Message = "probe failed: " + err.Error()
		return result
	}
	if len(addresses) == 0 {
		result.State = StateUnresolved
		result.Message = "the domain does not resolve"
		return result
	}
	if len(addresses) > maxAddresses {
		addresses = addresses[:maxAddresses]
	}

	result.Addresses = make([]AddressResult, len(addresses))
	var wg sync.WaitGroup
	for i, address := range addresses {
		wg.Add(1)
		go func(i int, ip net.IP) {
			defer wg.Done()
			result.Addresses[i] = p.probeAddress(ctx, client, target(ip), domain, perAddress)
			result.Addresses[i].Address = ip.String()
		}(i, address.IP)
	}
	wg.Wait()

	if parent.Err() != nil && !anyAnswered(result.Addresses) {
		// The caller ended the probe before any address answered: say
		// nothing rather than something wrong. The probe's own budget
		// expiring is a verdict, though: addresses that never answer within
		// it are unreachable, exactly like a firewall dropping port 80.
		result.State = StateUnknown
		result.Message = "probe failed: " + parent.Err().Error()
		return result
	}
	result.State, result.Message = aggregate(result.Addresses)
	return result
}

// probeAddress requests the identity route from one address. The body is
// never read: the verdict is the header alone.
func (p *Prober) probeAddress(ctx context.Context, client *http.Client, hostPort, domain string, timeout time.Duration) AddressResult {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+hostPort+edge.ProbePath, nil)
	if err != nil {
		return AddressResult{Outcome: OutcomeUnreachable, Detail: err.Error()}
	}
	request.Host = domain
	if p.UserAgent != "" {
		request.Header.Set("User-Agent", p.UserAgent)
	}
	response, err := client.Do(request)
	if err != nil {
		return AddressResult{Outcome: OutcomeUnreachable, Detail: describeError(err)}
	}
	response.Body.Close()
	if response.Header.Get(edge.InstanceHeader) == p.InstanceID {
		return AddressResult{Outcome: OutcomeOurs, Detail: "HTTP " + strconv.Itoa(response.StatusCode)}
	}
	detail := "HTTP " + strconv.Itoa(response.StatusCode) + " without " + edge.InstanceHeader
	if server := response.Header.Get("Server"); server != "" {
		detail += " (" + server + ")"
	}
	return AddressResult{Outcome: OutcomeForeign, Detail: detail}
}

// describeError keeps the operational part of a dial error and drops the
// Go plumbing around it.
func describeError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timed out"
	}
	message := err.Error()
	if i := strings.LastIndex(message, ": "); i >= 0 {
		message = message[i+2:]
	}
	return message
}

func anyAnswered(addresses []AddressResult) bool {
	for _, address := range addresses {
		if address.Outcome != OutcomeUnreachable {
			return true
		}
	}
	return false
}

// aggregate folds the address verdicts into one state and one sentence.
func aggregate(addresses []AddressResult) (State, string) {
	var ours, foreign []string
	for _, address := range addresses {
		switch address.Outcome {
		case OutcomeOurs:
			ours = append(ours, address.Address)
		case OutcomeForeign:
			foreign = append(foreign, address.Address)
		}
	}
	sort.Strings(foreign)
	total := len(addresses)
	switch {
	case len(ours) == total:
		if total == 1 {
			return StateReachable, "the address answers as this installation"
		}
		return StateReachable, fmt.Sprintf("all %d addresses answer as this installation", total)
	case len(ours) > 0 && len(foreign) == 0:
		return StateReachable, fmt.Sprintf("%d of %d addresses answer as this installation; the rest did not answer", len(ours), total)
	case len(ours) > 0:
		return StatePartial, fmt.Sprintf("%d of %d addresses answer as this installation; %s as another server", len(ours), total, answers(foreign))
	case len(foreign) > 0:
		return StateUnreachable, "no address answers as this installation; " + answers(foreign) + " as another server"
	case total == 1:
		return StateUnreachable, "the address did not answer at all"
	default:
		return StateUnreachable, fmt.Sprintf("none of the %d addresses answered at all", total)
	}
}

// answers lists addresses with the matching verb: "a answers" or "a, b answer".
func answers(addresses []string) string {
	if len(addresses) == 1 {
		return addresses[0] + " answers"
	}
	return strings.Join(addresses, ", ") + " answer"
}
