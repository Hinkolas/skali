package installer

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"

	"github.com/Hinkolas/skali/internal/installer/host"
)

// Network scopes name what an address is used for. A node advertises
// exactly one cluster address (the one other nodes reach it through) and
// any number of public addresses (the ones the internet reaches it
// through); a single-homed host uses the same address for both.
const (
	NetworkScopeCluster = "cluster"
	NetworkScopePublic  = "public"
)

// NodeNetwork is the operator's declaration of how one host is addressed.
// It drives four consumers that used to guess independently: the k3s
// node-ip, the k3s node-external-ip and certificate SANs, the coordinator
// listener set, and the endpoint other nodes are told to join through.
type NodeNetwork struct {
	// ClusterIP is the address other cluster nodes reach this node
	// through. Empty resolves to the address of the default route, which
	// is what k3s would have picked implicitly.
	ClusterIP string
	// PublicIPs are the addresses reachable from outside the cluster
	// network. They need not be assigned to the host: a floating or
	// NAT-mapped address is declared here and never bound.
	PublicIPs []string
	// ExtraSANs are additional names or addresses to place in the
	// Kubernetes API server certificate, for example a load balancer or
	// the DNS name operators use in kubeconfigs.
	ExtraSANs []string
	// CoordinatorBind selects which scopes the enrollment coordinator
	// listens on. Empty means cluster only.
	CoordinatorBind []string
}

// IsZero reports an empty declaration.
func (n NodeNetwork) IsZero() bool {
	return n.ClusterIP == "" && len(n.PublicIPs) == 0 && len(n.ExtraSANs) == 0 &&
		len(n.CoordinatorBind) == 0
}

// BindsPublic reports whether the coordinator should also listen on the
// public addresses.
func (n NodeNetwork) BindsPublic() bool {
	return slices.Contains(n.CoordinatorBind, NetworkScopePublic)
}

// Normalize trims, drops empties, and de-duplicates while preserving the
// operator's ordering.
func (n NodeNetwork) Normalize() NodeNetwork {
	return NodeNetwork{
		ClusterIP:       strings.TrimSpace(n.ClusterIP),
		PublicIPs:       normalizeList(n.PublicIPs),
		ExtraSANs:       normalizeList(n.ExtraSANs),
		CoordinatorBind: normalizeList(n.CoordinatorBind),
	}
}

func normalizeList(values []string) []string {
	var result []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || slices.Contains(result, value) {
			continue
		}
		result = append(result, value)
	}
	return result
}

// Validate checks shape only; whether the addresses exist on the host is a
// separate host probe.
func (n NodeNetwork) Validate() error {
	if n.ClusterIP != "" && net.ParseIP(n.ClusterIP) == nil {
		return fmt.Errorf("cluster address %q is not a valid IP address", n.ClusterIP)
	}
	for _, address := range n.PublicIPs {
		if net.ParseIP(address) == nil {
			return fmt.Errorf("public address %q is not a valid IP address", address)
		}
	}
	for _, san := range n.ExtraSANs {
		if net.ParseIP(san) == nil && !validDomain(san) {
			return fmt.Errorf("certificate name %q is neither an IP address nor a DNS name", san)
		}
	}
	for _, scope := range n.CoordinatorBind {
		if scope != NetworkScopeCluster && scope != NetworkScopePublic {
			return fmt.Errorf("coordinator bind scope must be %s or %s, got %q",
				NetworkScopeCluster, NetworkScopePublic, scope)
		}
	}
	if n.BindsPublic() && len(n.PublicIPs) == 0 {
		return errors.New("the coordinator cannot bind the public scope because no public addresses are declared")
	}
	return nil
}

// APIServerSANs is the certificate name set for this node's k3s API
// server: every address the API may be reached through, plus the
// operator's extra names. k3s adds the loopback, the service names, and
// the node's own hostname itself, so only the declaration appears here.
func (n NodeNetwork) APIServerSANs() []string {
	var sans []string
	for _, value := range append([]string{n.ClusterIP},
		append(append([]string(nil), n.PublicIPs...), n.ExtraSANs...)...) {
		value = strings.TrimSpace(value)
		if value == "" || slices.Contains(sans, value) {
			continue
		}
		sans = append(sans, value)
	}
	return sans
}

// HostAddress is one globally scoped address found on the host.
type HostAddress struct {
	IP        string
	Interface string
	// Private marks RFC1918, carrier-grade NAT, and IPv6 unique local
	// addresses: the ones that identify a private network rather than the
	// internet.
	Private bool
	// DefaultRoute marks the address the host sends internet traffic from.
	// It is the address k3s picks when node-ip is not set.
	DefaultRoute bool
}

// Label renders one address for a prompt or report line.
func (a HostAddress) Label() string {
	scope := "public"
	if a.Private {
		scope = "private network"
	}
	if a.DefaultRoute {
		scope += ", default route"
	}
	return fmt.Sprintf("%s (%s, %s)", a.IP, a.Interface, scope)
}

// containerInterfaces are the interface name prefixes owned by the
// container runtime, the CNI, and virtual bridges. Their addresses are
// never node addresses, and on an already-running node they would
// otherwise dominate the candidate list.
var containerInterfaces = []string{
	"lo", "cni", "flannel", "veth", "docker", "br-", "cali", "lxc", "tunl",
	"kube-ipvs", "dummy", "nodelocaldns", "vxlan",
}

// DetectHostAddresses lists the globally scoped addresses of the host's
// real interfaces, marking which one carries the default route. It runs
// through the runner so it reports the managed VM's addresses on macOS,
// not the Mac's.
func DetectHostAddresses(ctx context.Context, runner host.Runner) ([]HostAddress, error) {
	result, err := runner.Run(ctx, host.Command{
		Name: "ip", Args: []string{"-o", "addr", "show"},
	})
	if err != nil {
		return nil, fmt.Errorf("list host addresses: %w", err)
	}
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("list host addresses: ip exited %d: %s",
			result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	addresses := parseHostAddresses(result.Stdout)
	defaultIP := defaultRouteAddress(ctx, runner)
	for index := range addresses {
		if addresses[index].IP == defaultIP {
			addresses[index].DefaultRoute = true
		}
	}
	return addresses, nil
}

// parseHostAddresses reads `ip -o addr show` output. Lines look like
// "2: eth0    inet 10.0.1.2/32 metric 100 brd ... scope global eth0\ ...",
// so the scope keyword is located rather than indexed.
func parseHostAddresses(output string) []HostAddress {
	var addresses []HostAddress
	for line := range strings.SplitSeq(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		device, _, _ := strings.Cut(fields[1], "@")
		if fields[2] != "inet" && fields[2] != "inet6" {
			continue
		}
		if scopeOf(fields) != "global" || isContainerInterface(device) {
			continue
		}
		address, _, _ := strings.Cut(fields[3], "/")
		parsed := net.ParseIP(address)
		if parsed == nil || parsed.IsLoopback() || parsed.IsLinkLocalUnicast() ||
			isKubernetesAddress(parsed) {
			continue
		}
		if slices.ContainsFunc(addresses, func(existing HostAddress) bool {
			return existing.IP == address
		}) {
			continue
		}
		addresses = append(addresses, HostAddress{
			IP: address, Interface: device, Private: isPrivateIP(parsed),
		})
	}
	return addresses
}

func scopeOf(fields []string) string {
	for index, field := range fields {
		if field == "scope" && index+1 < len(fields) {
			return fields[index+1]
		}
	}
	return ""
}

func isContainerInterface(device string) bool {
	for _, prefix := range containerInterfaces {
		if device == prefix || strings.HasPrefix(device, prefix) {
			return true
		}
	}
	return false
}

// isKubernetesAddress excludes the k3s pod and service networks, whose
// bridge addresses are globally scoped and would otherwise look like a
// private network the operator could join through.
func isKubernetesAddress(ip net.IP) bool {
	for _, cidr := range []string{"10.42.0.0/16", "10.43.0.0/16"} {
		_, network, err := net.ParseCIDR(cidr)
		if err == nil && network.Contains(ip) {
			return true
		}
	}
	return false
}

func isPrivateIP(ip net.IP) bool {
	if ip.IsPrivate() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		// Carrier-grade NAT space, used by several providers for their
		// private networks.
		return v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127
	}
	return false
}

// defaultRouteAddress asks the kernel which source address internet
// traffic leaves from. An empty answer means the caller must decide
// without it.
func defaultRouteAddress(ctx context.Context, runner host.Runner) string {
	result, err := runner.Run(ctx, host.Command{
		Name: "ip", Args: []string{"route", "get", "1.1.1.1"},
	})
	if err != nil || result.ExitCode != 0 {
		return ""
	}
	fields := strings.Fields(result.Stdout)
	for index, field := range fields {
		if field == "src" && index+1 < len(fields) {
			return fields[index+1]
		}
	}
	return ""
}

// ResolveNodeNetwork completes an operator declaration against the host:
// the cluster address defaults to DefaultClusterAddress, and every
// declared cluster address must actually exist here (k3s refuses a node-ip
// it cannot find). Public addresses are deliberately not required to be
// local, because floating and NAT-mapped addresses are declared but never
// bound.
func ResolveNodeNetwork(ctx context.Context, runner host.Runner, desired NodeNetwork) (NodeNetwork, error) {
	resolved := desired.Normalize()
	if err := resolved.Validate(); err != nil {
		return resolved, err
	}
	addresses, err := DetectHostAddresses(ctx, runner)
	if err != nil {
		// Address detection is a convenience, not a gate: a host without
		// iproute2 keeps the k3s default (no declaration) or the operator's
		// explicit one, exactly as before this resolution existed.
		return resolved, nil
	}
	if resolved.ClusterIP == "" {
		resolved.ClusterIP = DefaultClusterAddress(addresses)
		return resolved, nil
	}
	if !slices.ContainsFunc(addresses, func(candidate HostAddress) bool {
		return candidate.IP == resolved.ClusterIP
	}) {
		return resolved, fmt.Errorf("cluster address %s is not assigned to this host; "+
			"assigned addresses are %s", resolved.ClusterIP, describeAddresses(addresses))
	}
	return resolved, nil
}

// DefaultClusterAddress picks the address a node advertises when the
// operator declared none; the interactive prompt recommends the same
// choice so a headless install and an answered prompt agree. Exactly
// one private address wins, because a private network is what an
// operator attaches cluster nodes to; otherwise the default route's
// address reproduces k3s's own implicit choice, and a host with a
// single address has no choice at all. Anything else is ambiguous and
// stays empty so the operator decides. Making the choice explicit is
// what puts it in the certificate SANs and in the endpoint other nodes
// are told to use.
func DefaultClusterAddress(addresses []HostAddress) string {
	private := ""
	privateCount := 0
	for _, address := range addresses {
		if address.Private {
			private = address.IP
			privateCount++
		}
	}
	if privateCount == 1 {
		return private
	}
	for _, address := range addresses {
		if address.DefaultRoute {
			return address.IP
		}
	}
	if len(addresses) == 1 {
		return addresses[0].IP
	}
	return ""
}

func describeAddresses(addresses []HostAddress) string {
	if len(addresses) == 0 {
		return "none"
	}
	values := make([]string, 0, len(addresses))
	for _, address := range addresses {
		values = append(values, address.IP+" ("+address.Interface+")")
	}
	return strings.Join(values, ", ")
}

// CoordinatorBindPlan is the resolved listener set for one coordinator.
type CoordinatorBindPlan struct {
	// Addresses are bound, in stable order.
	Addresses []string
	// Skipped are declared addresses that are not assigned to this host,
	// such as a floating public address. They are reported, never bound.
	Skipped []string
}

// PlanCoordinatorBind resolves which addresses the enrollment coordinator
// listens on. The wildcard is deliberately never used: k3s owns loopback
// port 6444 for local kube-apiserver access, so binding every address
// separately is what lets a multi-homed node answer on its private and its
// public address at once. advertised is the address the running k3s
// reports for this node; it is always included so live agents keep their
// endpoint working.
func PlanCoordinatorBind(ctx context.Context, runner host.Runner, record *Record,
	advertised string) (CoordinatorBindPlan, error) {
	var candidates []string
	if record != nil {
		candidates = append(candidates, record.Node.IP)
		if record.Node.Network().BindsPublic() {
			candidates = append(candidates, record.Node.PublicIPs...)
		}
	}
	candidates = append(candidates, advertised)

	local, err := DetectHostAddresses(ctx, runner)
	if err != nil {
		return CoordinatorBindPlan{}, err
	}
	plan := CoordinatorBindPlan{}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || net.ParseIP(candidate) == nil {
			continue
		}
		if slices.Contains(plan.Addresses, candidate) || slices.Contains(plan.Skipped, candidate) {
			continue
		}
		assigned := slices.ContainsFunc(local, func(address HostAddress) bool {
			return address.IP == candidate
		})
		if assigned {
			plan.Addresses = append(plan.Addresses, candidate)
		} else {
			plan.Skipped = append(plan.Skipped, candidate)
		}
	}
	if len(plan.Addresses) == 0 {
		return plan, fmt.Errorf("this node has no bindable coordinator address; "+
			"declared %s, assigned %s", strings.Join(candidates, ", "), describeAddresses(local))
	}
	return plan, nil
}

// ListeningAddresses reports the host's listening TCP sockets as
// "address:port" strings. It is used by diagnose to prove that an
// advertised endpoint is actually served.
func ListeningAddresses(ctx context.Context, runner host.Runner) ([]string, error) {
	result, err := runner.Run(ctx, host.Command{
		Name: "ss", Args: []string{"-H", "-lnt"},
	})
	if err != nil {
		return nil, fmt.Errorf("list listening sockets: %w", err)
	}
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("list listening sockets: ss exited %d: %s",
			result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	var listening []string
	for line := range strings.SplitSeq(result.Stdout, "\n") {
		fields := strings.Fields(line)
		// ss -H -lnt columns: State Recv-Q Send-Q Local Peer [Process]
		if len(fields) < 4 {
			continue
		}
		// Kept verbatim, brackets included: net.SplitHostPort is what reads
		// these back, and it needs the IPv6 form intact.
		local := fields[3]
		if !slices.Contains(listening, local) {
			listening = append(listening, local)
		}
	}
	return listening, nil
}

// EndpointServed reports whether one advertised host:port is covered by
// the listening set, honoring the wildcard binds.
func EndpointServed(listening []string, address, port string) bool {
	for _, entry := range listening {
		listenHost, listenPort, err := net.SplitHostPort(entry)
		if err != nil || listenPort != port {
			continue
		}
		switch listenHost {
		case address, "*", "0.0.0.0", "::", "":
			return true
		}
	}
	return false
}

// CoordinatorRouteAddress asks the target host's kernel which local address it
// uses for coordinator traffic. Discovery never substitutes a remote node IP.
// Multiple source addresses are ambiguous and require an operator choice.
func CoordinatorRouteAddress(ctx context.Context, runner host.Runner, endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	targets := []string{parsed.Hostname()}
	if net.ParseIP(targets[0]) == nil {
		result, err := runner.Run(ctx, host.Command{Name: "getent", Args: []string{"ahosts", targets[0]}})
		if err != nil || result.ExitCode != 0 {
			return ""
		}
		targets = nil
		for _, line := range strings.Split(result.Stdout, "\n") {
			fields := strings.Fields(line)
			if len(fields) > 0 && net.ParseIP(fields[0]) != nil && !slices.Contains(targets, fields[0]) {
				targets = append(targets, fields[0])
			}
		}
	}
	sources := []string{}
	for _, target := range targets {
		result, err := runner.Run(ctx, host.Command{Name: "ip", Args: []string{"route", "get", target}})
		if err != nil || result.ExitCode != 0 {
			continue
		}
		fields := strings.Fields(result.Stdout)
		for index, field := range fields {
			if field == "src" && index+1 < len(fields) && net.ParseIP(fields[index+1]) != nil && !slices.Contains(sources, fields[index+1]) {
				sources = append(sources, fields[index+1])
			}
		}
	}
	if len(sources) == 1 {
		return sources[0]
	}
	return ""
}
