package installer

import (
	"fmt"
	"net/netip"
	"strings"
)

// EdgeConfig configures the HTTP edge (Traefik) for the whole installation.
// It is both the init.yaml block and the recorded form: present in an init
// configuration it replaces the recorded settings, absent it keeps them.
type EdgeConfig struct {
	ProxyProtocol *ProxyProtocolConfig `yaml:"proxyProtocol,omitempty" json:"proxyProtocol,omitempty" jsonschema:"PROXY protocol from a TCP load balancer in front of the edge nodes. Omit when clients connect to the edge nodes directly."`
}

// ProxyProtocolConfig names the load balancer allowed to report the
// visitor's address in a PROXY protocol header.
type ProxyProtocolConfig struct {
	TrustedIPs []string `yaml:"trustedIPs" json:"trustedIPs" jsonschema:"Addresses (IPs or CIDRs) the load balancer connects to the edge nodes from, for example its private network address. Only connections from these addresses may carry a PROXY protocol header; others are served with their own address. The header is optional from them, so enable it on the balancer after this setting is applied."`
}

// ProxyProtocolTrustedIPs returns the recorded balancer addresses, nil when
// PROXY protocol is off.
func (e *EdgeConfig) ProxyProtocolTrustedIPs() []string {
	if e == nil || e.ProxyProtocol == nil {
		return nil
	}
	return e.ProxyProtocol.TrustedIPs
}

// kubernetesNetworks are k3s's default pod and service networks. Trusting
// any part of them would let an application pod, which can open
// connections to Traefik from its own pod address, forge a visitor address
// in a PROXY header.
var kubernetesNetworks = []netip.Prefix{
	netip.MustParsePrefix("10.42.0.0/16"),
	netip.MustParsePrefix("10.43.0.0/16"),
}

// normalizeEdgeConfig validates an edge configuration and returns its
// canonical form: trusted addresses as masked CIDRs, in authored order,
// without duplicates. A configuration that enables nothing normalizes to
// nil, so the record omits it and the bundle hash stays where it was.
func normalizeEdgeConfig(edge *EdgeConfig) (*EdgeConfig, error) {
	trusted, err := normalizeProxyProtocolTrust(edge.ProxyProtocolTrustedIPs())
	if err != nil {
		return nil, err
	}
	if len(trusted) == 0 {
		return nil, nil
	}
	return &EdgeConfig{ProxyProtocol: &ProxyProtocolConfig{TrustedIPs: trusted}}, nil
}

// recordedEdge returns the installation's current edge settings: the
// published in-cluster record's when it belongs to this installation, the
// local record's before the first publication.
func recordedEdge(local, published *Record) *EdgeConfig {
	if published != nil && published.InstallationID == local.InstallationID {
		return published.Edge
	}
	return local.Edge
}

// resolveEdgeConfig picks the edge settings an init run records: only an
// explicit override changes them, so upgrades and headless re-runs keep a
// configured balancer, and an override that enables nothing turns them
// off.
func resolveEdgeConfig(recorded, override *EdgeConfig) (*EdgeConfig, error) {
	if override == nil {
		return recorded, nil
	}
	return normalizeEdgeConfig(override)
}

func normalizeProxyProtocolTrust(entries []string) ([]string, error) {
	var trusted []string
	seen := make(map[netip.Prefix]struct{}, len(entries))
	for _, entry := range entries {
		raw := strings.TrimSpace(entry)
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			address, addrErr := netip.ParseAddr(raw)
			if addrErr != nil || address.Zone() != "" {
				return nil, fmt.Errorf("proxy protocol trusted address %q is not an IP address or CIDR", entry)
			}
			address = address.Unmap()
			prefix = netip.PrefixFrom(address, address.BitLen())
		}
		if prefix.Addr().Is4In6() || prefix.Addr().Zone() != "" {
			return nil, fmt.Errorf("proxy protocol trusted address %q must be a plain IPv4 or IPv6 address or CIDR", entry)
		}
		prefix = prefix.Masked()
		if prefix.Bits() == 0 {
			return nil, fmt.Errorf("proxy protocol trusted address %q trusts every address; "+
				"name the load balancer's own addresses", entry)
		}
		for _, network := range kubernetesNetworks {
			if prefix.Overlaps(network) {
				return nil, fmt.Errorf("proxy protocol trusted address %q overlaps the cluster's internal network %s, "+
					"from which application pods could forge client addresses", entry, network)
			}
		}
		if _, ok := seen[prefix]; ok {
			continue
		}
		seen[prefix] = struct{}{}
		trusted = append(trusted, prefix.String())
	}
	return trusted, nil
}
