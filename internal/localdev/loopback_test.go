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
