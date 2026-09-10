package localdev

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWithGatewayEntry(t *testing.T) {
	t.Parallel()

	// Captured from a live node: docker regenerated /etc/hosts without the
	// k3d-injected gateway entry.
	regenerated := "127.0.0.1\tlocalhost\n::1\tlocalhost ip6-localhost ip6-loopback\n172.18.0.2\tskali-dev\n"
	updated, changed := withGatewayEntry(regenerated, "192.168.65.254")
	require.True(t, changed)
	require.Equal(t, regenerated+"192.168.65.254 host.k3d.internal\n", updated)

	// A correct entry leaves the content byte-identical.
	updated, changed = withGatewayEntry(updated, "192.168.65.254")
	require.False(t, changed)
	require.Equal(t, regenerated+"192.168.65.254 host.k3d.internal\n", updated)

	// A stale address is replaced, not duplicated.
	stale := "127.0.0.1 localhost\n192.168.65.2 host.k3d.internal\n"
	updated, changed = withGatewayEntry(stale, "192.168.65.254")
	require.True(t, changed)
	require.Equal(t, "127.0.0.1 localhost\n192.168.65.254 host.k3d.internal\n", updated)

	// NodeHosts shape: k3s rewrote the key down to the node entry alone.
	nodeHosts := "172.18.0.2 skali-dev\n"
	updated, changed = withGatewayEntry(nodeHosts, "192.168.65.254")
	require.True(t, changed)
	require.Equal(t, "172.18.0.2 skali-dev\n192.168.65.254 host.k3d.internal\n", updated)

	// Empty content still gains the entry.
	updated, changed = withGatewayEntry("", "192.168.65.254")
	require.True(t, changed)
	require.Equal(t, "192.168.65.254 host.k3d.internal\n", updated)

	// Duplicate entries collapse to the one correct line.
	duplicated := "192.168.65.254 host.k3d.internal\n192.168.65.2 host.k3d.internal\n"
	updated, changed = withGatewayEntry(duplicated, "192.168.65.254")
	require.True(t, changed)
	require.Equal(t, "192.168.65.254 host.k3d.internal\n", updated)
}

func TestPingResolvedIP(t *testing.T) {
	t.Parallel()
	require.Equal(t, "192.168.65.254",
		pingResolvedIP("PING host.docker.internal (192.168.65.254): 56 data bytes\n"))
	require.Equal(t, "", pingResolvedIP("ping: bad address 'host.docker.internal'\n"))
	require.Equal(t, "", pingResolvedIP(""))
}

func TestNslookupResolved(t *testing.T) {
	answer := "Server:\t\t10.43.0.10\nAddress:\t10.43.0.10:53\n\nName:\thost.k3d.internal\nAddress: 192.168.65.254\n"
	if !nslookupResolved(answer, "host.k3d.internal", "192.168.65.254") {
		t.Error("resolved answer not recognized")
	}
	if nslookupResolved(answer, "host.k3d.internal", "10.43.0.10") {
		t.Error("the server banner address must not count as the answer")
	}
	nx := "Server:\t\t10.43.0.10\nAddress:\t10.43.0.10:53\n\nNon-authoritative answer:\n\n** server can't find host.k3d.internal: NXDOMAIN\n"
	if nslookupResolved(nx, "host.k3d.internal", "192.168.65.254") {
		t.Error("NXDOMAIN taken for a resolution")
	}
	stale := "Name:\thost.k3d.internal\nAddress: 172.17.0.1\n"
	if nslookupResolved(stale, "host.k3d.internal", "192.168.65.254") {
		t.Error("a stale address must not count")
	}
}
