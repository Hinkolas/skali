package installer

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeEdgeConfig(t *testing.T) {
	t.Parallel()
	trust := func(entries ...string) *EdgeConfig {
		return &EdgeConfig{ProxyProtocol: &ProxyProtocolConfig{TrustedIPs: entries}}
	}

	normalized, err := normalizeEdgeConfig(trust(" 10.0.0.2 ", "203.0.113.0/24", "2001:db8::1", "10.0.0.2/32", "192.168.1.7/24"))
	require.NoError(t, err)
	require.Equal(t, []string{"10.0.0.2/32", "203.0.113.0/24", "2001:db8::1/128", "192.168.1.0/24"},
		normalized.ProxyProtocolTrustedIPs(), "addresses become single-host CIDRs, prefixes are masked, duplicates dropped")

	mapped, err := normalizeEdgeConfig(trust("::ffff:10.0.0.2"))
	require.NoError(t, err)
	require.Equal(t, []string{"10.0.0.2/32"}, mapped.ProxyProtocolTrustedIPs(), "a mapped address is its IPv4 address")

	for _, empty := range []*EdgeConfig{{}, {ProxyProtocol: &ProxyProtocolConfig{}}, trust()} {
		normalized, err := normalizeEdgeConfig(empty)
		require.NoError(t, err)
		require.Nil(t, normalized, "a block enabling nothing records nothing")
	}

	for entry, message := range map[string]string{
		"0.0.0.0/0":           "trusts every address",
		"::/0":                "trusts every address",
		"10.42.3.4":           "overlaps the cluster's internal network 10.42.0.0/16",
		"10.43.0.0/24":        "overlaps the cluster's internal network 10.43.0.0/16",
		"10.0.0.0/8":          "overlaps the cluster's internal network",
		"lb.example.com":      "is not an IP address or CIDR",
		"::ffff:10.0.0.0/120": "must be a plain IPv4 or IPv6",
		"fe80::1%eth0":        "is not an IP address or CIDR",
		"10.0.0.2/33":         "is not an IP address or CIDR",
	} {
		_, err := normalizeEdgeConfig(trust(entry))
		require.ErrorContains(t, err, message, entry)
	}
}

func TestResolveEdgeConfig(t *testing.T) {
	t.Parallel()
	recorded := &EdgeConfig{ProxyProtocol: &ProxyProtocolConfig{TrustedIPs: []string{"10.0.0.2/32"}}}

	kept, err := resolveEdgeConfig(recorded, nil)
	require.NoError(t, err)
	require.Same(t, recorded, kept, "upgrades and runs without an edge option keep the balancer")

	off, err := resolveEdgeConfig(recorded, &EdgeConfig{})
	require.NoError(t, err)
	require.Nil(t, off, "an explicit empty block turns PROXY protocol off")

	replaced, err := resolveEdgeConfig(recorded,
		&EdgeConfig{ProxyProtocol: &ProxyProtocolConfig{TrustedIPs: []string{"10.0.0.3"}}})
	require.NoError(t, err)
	require.Equal(t, []string{"10.0.0.3/32"}, replaced.ProxyProtocolTrustedIPs())

	_, err = resolveEdgeConfig(recorded, &EdgeConfig{ProxyProtocol: &ProxyProtocolConfig{TrustedIPs: []string{"0.0.0.0/0"}}})
	require.Error(t, err)
}

// A reconciled cluster may re-run init from a second server whose local
// record never saw the edge settings; the published record decides.
func TestRecordedEdgePrefersPublishedRecord(t *testing.T) {
	t.Parallel()
	balancer := &EdgeConfig{ProxyProtocol: &ProxyProtocolConfig{TrustedIPs: []string{"10.0.0.2/32"}}}
	local := &Record{InstallationID: "install-1"}
	published := &Record{InstallationID: "install-1", Edge: balancer}

	require.Same(t, balancer, recordedEdge(local, published))
	require.Nil(t, recordedEdge(local, nil), "before the first publication the local record decides")
	require.Nil(t, recordedEdge(local, &Record{InstallationID: "other", Edge: balancer}),
		"another installation's record is never adopted")

	kept, err := resolveEdgeConfig(recordedEdge(local, published), nil)
	require.NoError(t, err)
	require.Equal(t, []string{"10.0.0.2/32"}, kept.ProxyProtocolTrustedIPs())
}
