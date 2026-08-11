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

func TestPortBindingsHaveLoopback(t *testing.T) {
	t.Parallel()
	// The docker-inspect shape as Docker Desktop reports it.
	inspect := []byte(`[{"HostConfig":{"PortBindings":{
		"30500/tcp":[{"HostIp":"127.0.0.1","HostPort":"5510"}],
		"30510/tcp":[{"HostIp":"127.0.0.1","HostPort":"30510"}],
		"80/tcp":[{"HostIp":"127.0.0.1","HostPort":"8080"}]}}}]`)

	has, err := portBindingsHaveLoopback(inspect, 30510, 30510)
	require.NoError(t, err)
	require.True(t, has)

	// Wrong host port, missing container port, and malformed input.
	has, err = portBindingsHaveLoopback(inspect, 30510, 45009)
	require.NoError(t, err)
	require.False(t, has)
	has, err = portBindingsHaveLoopback(inspect, 30509, 30509)
	require.NoError(t, err)
	require.False(t, has)
	_, err = portBindingsHaveLoopback([]byte("nonsense"), 30510, 30510)
	require.Error(t, err)
	_, err = portBindingsHaveLoopback([]byte("[]"), 30510, 30510)
	require.Error(t, err)
}
