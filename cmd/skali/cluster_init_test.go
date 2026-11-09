package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/installer"
)

func TestProxyProtocolFlag(t *testing.T) {
	t.Parallel()
	edge, err := proxyProtocolFlag(false, "")
	require.NoError(t, err)
	require.Nil(t, edge, "an unset flag keeps the recorded settings")

	edge, err = proxyProtocolFlag(true, "none")
	require.NoError(t, err)
	require.Equal(t, &installer.EdgeConfig{}, edge, "none turns PROXY protocol off explicitly")

	edge, err = proxyProtocolFlag(true, " 10.0.0.2, ,2001:db8::/64 ")
	require.NoError(t, err)
	require.Equal(t, []string{"10.0.0.2", "2001:db8::/64"}, edge.ProxyProtocolTrustedIPs())

	_, err = proxyProtocolFlag(true, " ")
	require.ErrorContains(t, err, "or none")
}
