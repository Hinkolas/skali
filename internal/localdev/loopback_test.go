package localdev

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoopbackPortArgs(t *testing.T) {
	args := loopbackPortArgs()
	require.Len(t, args, 2*loopbackNodePortCount)
	require.Equal(t, "-p", args[0])
	require.Equal(t, "127.0.0.1:30501:30501@server:0:direct", args[1])
	require.Equal(t, "127.0.0.1:30510:30510@server:0:direct", args[len(args)-1])

	t.Setenv("SKALI_DEV_LOOPBACK_PORT_BASE", "45000")
	shifted := loopbackPortArgs()
	require.Equal(t, "127.0.0.1:45000:30501@server:0:direct", shifted[1])
	require.Equal(t, "127.0.0.1:45009:30510@server:0:direct", shifted[len(shifted)-1])
}

// The edge is published on both entrypoints, bound to loopback, on the
// default web ports unless the suite overrides shift them.
func TestCreateArgsPublishBothEdgeEntrypoints(t *testing.T) {
	args := createArgs("/state/registries.yaml")
	require.Contains(t, args, "127.0.0.1:80:80@server:0:direct")
	require.Contains(t, args, "127.0.0.1:443:443@server:0:direct")
	require.Contains(t, args, "127.0.0.1:5510:30500@server:0:direct")
	require.Equal(t, "--wait", args[len(args)-1])

	t.Setenv("SKALI_DEV_HTTP_PORT", "8082")
	t.Setenv("SKALI_DEV_HTTPS_PORT", "8443")
	shifted := createArgs("/state/registries.yaml")
	require.Contains(t, shifted, "127.0.0.1:8082:80@server:0:direct")
	require.Contains(t, shifted, "127.0.0.1:8443:443@server:0:direct")
}

func TestReservedHostPortsIncludeBothEdgePorts(t *testing.T) {
	ports := ReservedHostPorts()
	require.Contains(t, ports, 80)
	require.Contains(t, ports, 443)
	require.Contains(t, ports, 5510)
}

func TestMasterURLOmitsTheDefaultPort(t *testing.T) {
	require.Equal(t, "https://skali.localhost", MasterURL())
	t.Setenv("SKALI_DEV_HTTPS_PORT", "8443")
	require.Equal(t, "https://skali.localhost:8443", MasterURL())
}
