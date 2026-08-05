package installer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/installer/host"
)

// hetznerAddresses reproduces a cloud server with a public interface
// carrying the default route and a private network interface, plus the
// bridges an already-running k3s adds.
const hetznerAddresses = `1: lo    inet 127.0.0.1/8 scope host lo\       valid_lft forever preferred_lft forever
2: eth0    inet 203.0.113.7/32 metric 100 scope global dynamic eth0\       valid_lft 84559sec
3: enp7s0    inet 10.0.1.2/32 metric 200 brd 10.0.1.2 scope global dynamic enp7s0\       valid_lft 84559sec
4: cni0    inet 10.42.0.1/24 brd 10.42.0.255 scope global cni0\       valid_lft forever
5: flannel.1    inet 10.42.0.0/32 scope global flannel.1\       valid_lft forever
6: veth1234@if3    inet6 fe80::1/64 scope link \       valid_lft forever
2: eth0    inet6 2001:db8::7/64 scope global dynamic \       valid_lft forever
`

func addressFake(output, route string) *host.Fake {
	return &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"ip": func(cmd host.Command) (host.Result, error) {
			if len(cmd.Args) > 0 && cmd.Args[0] == "route" {
				return host.Result{Stdout: route}, nil
			}
			return host.Result{Stdout: output}, nil
		},
	}}
}

func TestDetectHostAddresses(t *testing.T) {
	t.Parallel()
	fake := addressFake(hetznerAddresses,
		"1.1.1.1 via 203.0.113.1 dev eth0 src 203.0.113.7 uid 0\n    cache\n")

	addresses, err := DetectHostAddresses(context.Background(), fake)

	require.NoError(t, err)
	require.Equal(t, []HostAddress{
		{IP: "203.0.113.7", Interface: "eth0", DefaultRoute: true},
		{IP: "10.0.1.2", Interface: "enp7s0", Private: true},
		{IP: "2001:db8::7", Interface: "eth0"},
	}, addresses)
}

// The pod and service networks are globally scoped and private, so without
// an explicit exclusion they would look like a private network the
// operator could put cluster traffic on.
func TestDetectHostAddressesExcludesClusterNetworks(t *testing.T) {
	t.Parallel()
	fake := addressFake(hetznerAddresses, "")

	addresses, err := DetectHostAddresses(context.Background(), fake)

	require.NoError(t, err)
	for _, address := range addresses {
		require.NotContains(t, []string{"10.42.0.1", "10.42.0.0"}, address.IP)
		require.NotEqual(t, "cni0", address.Interface)
	}
}

func TestResolveNodeNetworkDefaultsToSolePrivateAddress(t *testing.T) {
	t.Parallel()
	fake := addressFake(hetznerAddresses,
		"1.1.1.1 via 203.0.113.1 dev eth0 src 203.0.113.7 uid 0\n")

	resolved, err := ResolveNodeNetwork(context.Background(), fake, NodeNetwork{})

	require.NoError(t, err)
	require.Equal(t, "10.0.1.2", resolved.ClusterIP,
		"an undeclared address resolves to the sole private address, the same choice the prompt recommends")
}

// publicOnlyAddresses reproduces a server without a private network:
// two public interfaces, one carrying the default route.
const publicOnlyAddresses = `1: lo    inet 127.0.0.1/8 scope host lo\       valid_lft forever
2: eth0    inet 203.0.113.7/32 metric 100 scope global dynamic eth0\       valid_lft 84559sec
3: eth1    inet 198.51.100.4/32 scope global eth1\       valid_lft forever
`

func TestResolveNodeNetworkFallsBackToDefaultRoute(t *testing.T) {
	t.Parallel()
	fake := addressFake(publicOnlyAddresses,
		"1.1.1.1 via 203.0.113.1 dev eth0 src 203.0.113.7 uid 0\n")

	resolved, err := ResolveNodeNetwork(context.Background(), fake, NodeNetwork{})

	require.NoError(t, err)
	require.Equal(t, "203.0.113.7", resolved.ClusterIP,
		"without a private network the undeclared address is the one k3s would have picked")
}

func TestDefaultClusterAddressAmbiguity(t *testing.T) {
	t.Parallel()
	require.Empty(t, DefaultClusterAddress(nil))
	require.Empty(t, DefaultClusterAddress([]HostAddress{
		{IP: "10.0.1.2", Private: true},
		{IP: "10.0.2.2", Private: true},
	}), "two private addresses without a default route are ambiguous")
}

func TestResolveNodeNetworkRefusesForeignClusterAddress(t *testing.T) {
	t.Parallel()
	fake := addressFake(hetznerAddresses, "")

	_, err := ResolveNodeNetwork(context.Background(), fake,
		NodeNetwork{ClusterIP: "10.0.9.9"})

	require.ErrorContains(t, err, "not assigned to this host")
	require.ErrorContains(t, err, "10.0.1.2 (enp7s0)")
}

// A floating or NAT-mapped public address is declared but never assigned
// locally; refusing it would make the common cloud shape unusable.
func TestResolveNodeNetworkAllowsForeignPublicAddress(t *testing.T) {
	t.Parallel()
	fake := addressFake(hetznerAddresses, "")

	resolved, err := ResolveNodeNetwork(context.Background(), fake, NodeNetwork{
		ClusterIP: "10.0.1.2", PublicIPs: []string{"198.51.100.42"},
	})

	require.NoError(t, err)
	require.Equal(t, []string{"198.51.100.42"}, resolved.PublicIPs)
}

func TestNodeNetworkValidate(t *testing.T) {
	t.Parallel()
	require.ErrorContains(t, NodeNetwork{ClusterIP: "nope"}.Validate(),
		"not a valid IP address")
	require.ErrorContains(t, NodeNetwork{PublicIPs: []string{"nope"}}.Validate(),
		"not a valid IP address")
	require.ErrorContains(t, NodeNetwork{ExtraSANs: []string{"not a name"}}.Validate(),
		"neither an IP address nor a DNS name")
	require.ErrorContains(t, NodeNetwork{CoordinatorBind: []string{"internal"}}.Validate(),
		"coordinator bind scope must be")
	require.ErrorContains(t, NodeNetwork{
		ClusterIP: "10.0.1.2", CoordinatorBind: []string{NetworkScopePublic},
	}.Validate(), "no public addresses are declared")
	require.NoError(t, NodeNetwork{
		ClusterIP: "10.0.1.2", PublicIPs: []string{"203.0.113.7"},
		ExtraSANs:       []string{"cluster.example.com"},
		CoordinatorBind: []string{NetworkScopeCluster, NetworkScopePublic},
	}.Validate())
}

func TestPlanCoordinatorBind(t *testing.T) {
	t.Parallel()
	record := &Record{Node: NodeRecord{Name: "cp-1", Role: "server"}}
	record.Node.SetNetwork(NodeNetwork{
		ClusterIP: "10.0.1.2", PublicIPs: []string{"203.0.113.7", "198.51.100.42"},
		CoordinatorBind: []string{NetworkScopeCluster, NetworkScopePublic},
	})
	fake := addressFake(hetznerAddresses, "")

	plan, err := PlanCoordinatorBind(context.Background(), fake, record, "203.0.113.7")

	require.NoError(t, err)
	require.Equal(t, []string{"10.0.1.2", "203.0.113.7"}, plan.Addresses)
	require.Equal(t, []string{"198.51.100.42"}, plan.Skipped,
		"an address that is not local is reported, never bound")
}

func TestEndpointServed(t *testing.T) {
	t.Parallel()
	listening := []string{"203.0.113.7:6444", "127.0.0.1:6444", "*:6443", "[::]:10250"}
	require.True(t, EndpointServed(listening, "fd00::2", "10250"),
		"an IPv6 wildcard bind is read back through its bracketed form")

	require.True(t, EndpointServed(listening, "203.0.113.7", "6444"))
	require.False(t, EndpointServed(listening, "10.0.1.2", "6444"),
		"the address a joining node is told to use must be bound, not merely present elsewhere")
	require.True(t, EndpointServed(listening, "10.0.1.2", "6443"),
		"a wildcard bind serves every address")
	require.True(t, EndpointServed(listening, "10.0.1.2", "10250"))
}

func TestListeningAddresses(t *testing.T) {
	t.Parallel()
	fake := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"ss": func(host.Command) (host.Result, error) {
			return host.Result{Stdout: `LISTEN 0      4096      127.0.0.1:6444       0.0.0.0:*
LISTEN 0      4096              *:6443             *:*
LISTEN 0      4096   [::1]:10248          [::]:*
`}, nil
		},
	}}

	listening, err := ListeningAddresses(context.Background(), fake)

	require.NoError(t, err)
	require.Equal(t, []string{"127.0.0.1:6444", "*:6443", "[::1]:10248"}, listening)
}
