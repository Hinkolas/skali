package installer

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/layout"
)

func TestJoinTokenRoundtrip(t *testing.T) {
	t.Parallel()
	token := encodeJoinToken("K10abc::node:secret", "pull-secret-value", layout.RoleAgent)
	require.True(t, strings.HasPrefix(token, joinTokenPrefix))
	k3sToken, pullSecret, role, err := decodeJoinToken(token)
	require.NoError(t, err)
	require.Equal(t, "K10abc::node:secret", k3sToken)
	require.Equal(t, "pull-secret-value", pullSecret)
	require.Equal(t, layout.RoleAgent, role)
}

func TestJoinTokenServerRole(t *testing.T) {
	t.Parallel()
	token := encodeJoinToken("K10abc::server:secret", "pull-secret-value", layout.RoleServer)
	_, _, role, err := decodeJoinToken(token)
	require.NoError(t, err)
	require.Equal(t, layout.RoleServer, role)
}

func TestJoinTokenRoutingClaims(t *testing.T) {
	t.Parallel()
	token := encodeJoinTokenWithClaims(
		"K10abc::server:secret",
		"pull-secret-value",
		layout.RoleServer,
		"e2e",
		"https://10.1.0.3:6443",
	)
	decoded, err := decodeJoinTokenClaims(token)
	require.NoError(t, err)
	require.True(t, decoded.Composite)
	require.Equal(t, "e2e", decoded.Cluster)
	require.Equal(t, "https://10.1.0.3:6443", decoded.Server)

	claims, err := InspectJoinToken(token)
	require.NoError(t, err)
	require.Equal(t, JoinTokenClaims{
		Cluster: "e2e",
		Server:  "https://10.1.0.3:6443",
		Role:    layout.RoleServer,
	}, claims)
}

func TestJoinTokenRawFallback(t *testing.T) {
	t.Parallel()
	// A manually minted secure k3s token passes through untouched, with
	// no pull credential or routing claims; the installer requires those
	// missing claims to be supplied explicitly.
	k3sToken, pullSecret, role, err := decodeJoinToken("K10abc::node:secret")
	require.NoError(t, err)
	require.Equal(t, "K10abc::node:secret", k3sToken)
	require.Empty(t, pullSecret)
	require.Empty(t, role)
}

func TestJoinTokenMalformed(t *testing.T) {
	t.Parallel()
	for name, token := range map[string]string{
		"bad base64": joinTokenPrefix + "not-base64!!!",
		"bad json":   joinTokenPrefix + "bm90IGpzb24",
		"no k3s":     encodeJoinToken("", "pull-only", ""),
	} {
		_, _, _, err := decodeJoinToken(token)
		require.ErrorContains(t, err, "malformed skali join token", name)
	}
}

func TestNewPullSecret(t *testing.T) {
	t.Parallel()
	first, err := newPullSecret()
	require.NoError(t, err)
	second, err := newPullSecret()
	require.NoError(t, err)
	require.Len(t, first, 43, "32 random bytes in unpadded base64url")
	require.NotEqual(t, first, second)
}
