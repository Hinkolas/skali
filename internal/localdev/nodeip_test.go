package localdev

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNetworkAndAliasNames(t *testing.T) {
	require.Equal(t, "k3d-skali-dev", networkName())
	require.Equal(t, "k3d-skali-dev-server-0", serverAlias())

	t.Setenv("SKALI_DEV_CLUSTER", "skali-dev-e2e")
	require.Equal(t, "k3d-skali-dev-e2e", networkName())
	require.Equal(t, "k3d-skali-dev-e2e-server-0", serverAlias())
}

func TestParseNodeState(t *testing.T) {
	t.Parallel()
	// Captured from a live crash-looping node mid-restart.
	restarting := []byte(`[{"State":{"Running":true,"Restarting":true,"ExitCode":1},"RestartCount":11}]`)
	state, err := parseNodeState(restarting)
	require.NoError(t, err)
	require.Equal(t, nodeState{Running: true, Restarting: true, RestartCount: 11, ExitCode: 1}, state)

	running := []byte(`[{"State":{"Running":true,"Restarting":false,"ExitCode":0},"RestartCount":0}]`)
	state, err = parseNodeState(running)
	require.NoError(t, err)
	require.Equal(t, nodeState{Running: true}, state)

	_, err = parseNodeState([]byte("nonsense"))
	require.Error(t, err)
	_, err = parseNodeState([]byte("[]"))
	require.Error(t, err)
}

func TestParseNodeEndpoint(t *testing.T) {
	t.Parallel()
	// Captured from a pre-pin cluster: attached, no static address.
	unpinned := []byte(`[{"NetworkSettings":{"Networks":{"k3d-skali-dev":{"IPAMConfig":null,"IPAddress":"172.18.0.2"}}}}]`)
	endpoint, err := parseNodeEndpoint(unpinned, "k3d-skali-dev")
	require.NoError(t, err)
	require.Equal(t, nodeEndpoint{IP: "172.18.0.2"}, endpoint)

	pinned := []byte(`[{"NetworkSettings":{"Networks":{"k3d-skali-dev":{"IPAMConfig":{"IPv4Address":"172.18.0.3"},"IPAddress":"172.18.0.3"}}}}]`)
	endpoint, err = parseNodeEndpoint(pinned, "k3d-skali-dev")
	require.NoError(t, err)
	require.Equal(t, nodeEndpoint{IP: "172.18.0.3", PinnedIP: "172.18.0.3"}, endpoint)

	_, err = parseNodeEndpoint(unpinned, "k3d-other")
	require.Error(t, err)
	_, err = parseNodeEndpoint([]byte("[]"), "k3d-skali-dev")
	require.Error(t, err)
}

func TestParseNetworkSpec(t *testing.T) {
	t.Parallel()
	// Captured from a live k3d cluster network (container IDs shortened).
	inspect := []byte(`[{
		"IPAM":{"Driver":"default","Options":null,"Config":[{"Subnet":"172.18.0.0/16","Gateway":"172.18.0.1"}]},
		"Labels":{"app":"k3d"},
		"Options":{"com.docker.network.bridge.enable_ip_masquerade":"true"},
		"Containers":{
			"8d3743125c44":{"Name":"skali-dev","IPv4Address":"172.18.0.3/16"},
			"11aa22bb33cc":{"Name":"k3d-skali-dev-tools","IPv4Address":"172.18.0.2/16"}}}]`)

	spec, err := parseNetworkSpec(inspect, "skali-dev")
	require.NoError(t, err)
	require.Equal(t, "172.18.0.0/16", spec.Subnet)
	require.Equal(t, "172.18.0.1", spec.Gateway)
	require.Equal(t, map[string]string{"app": "k3d"}, spec.Labels)
	require.Equal(t, map[string]string{"com.docker.network.bridge.enable_ip_masquerade": "true"}, spec.Options)
	// The node itself is filtered; other members keep their bare address.
	require.Equal(t, map[string]string{"k3d-skali-dev-tools": "172.18.0.2"}, spec.Members)

	_, err = parseNetworkSpec([]byte("nonsense"), "skali-dev")
	require.Error(t, err)
	_, err = parseNetworkSpec([]byte("[]"), "skali-dev")
	require.Error(t, err)
}

func TestFirstNodeIP(t *testing.T) {
	t.Parallel()
	// The real kubelet line from a k3s boot log.
	kubelet := `time="2026-08-12T08:22:07Z" level=info msg="Running kubelet --cloud-provider=external ` +
		`--hostname-override=k3d-skali-dev-server-0 --node-ip=172.18.0.3 --node-labels="`
	require.Equal(t, "172.18.0.3", firstNodeIP(kubelet))
	require.Equal(t, "", firstNodeIP(`level=info msg="Starting k3s v1.36.3+k3s1"`))
	require.Equal(t, "", firstNodeIP(""))
}

func TestHasNodeIPFatal(t *testing.T) {
	t.Parallel()
	fatal := `time="2026-08-23T10:42:57Z" level=fatal msg="Failed to start networking: ` +
		`unable to initialize network policy controller: error getting node subnet: ` +
		`failed to find interface with specified node ip"`
	require.True(t, hasNodeIPFatal(fatal))
	require.False(t, hasNodeIPFatal(`level=info msg="Tunnel server egress proxy mode: agent"`))
	require.False(t, hasNodeIPFatal(""))
}

func TestNodeUnhealthyError(t *testing.T) {
	t.Parallel()
	err := &NodeUnhealthyError{Cluster: "skali-dev", Diagnosis: "its apiserver did not answer"}
	message := err.Error()
	require.Contains(t, message, "skali-dev")
	require.Contains(t, message, "skali dev reset")
	for _, r := range message {
		require.Less(t, r, rune(128), "error message must be plain ascii: %q", message)
	}
}
