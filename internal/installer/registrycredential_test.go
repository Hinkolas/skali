package installer

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestJoinTokenRoundtrip(t *testing.T) {
	t.Parallel()
	token := encodeJoinToken("K10abc::node:secret", "pull-secret-value")
	require.True(t, strings.HasPrefix(token, joinTokenPrefix))
	k3sToken, pullSecret, err := decodeJoinToken(token)
	require.NoError(t, err)
	require.Equal(t, "K10abc::node:secret", k3sToken)
	require.Equal(t, "pull-secret-value", pullSecret)
}

func TestJoinTokenRawFallback(t *testing.T) {
	t.Parallel()
	// A manually minted k3s token passes through untouched, with no pull
	// credential.
	k3sToken, pullSecret, err := decodeJoinToken("K10abc::node:secret")
	require.NoError(t, err)
	require.Equal(t, "K10abc::node:secret", k3sToken)
	require.Empty(t, pullSecret)
}

func TestJoinTokenMalformed(t *testing.T) {
	t.Parallel()
	for name, token := range map[string]string{
		"bad base64": joinTokenPrefix + "not-base64!!!",
		"bad json":   joinTokenPrefix + "bm90IGpzb24",
		"no k3s":     encodeJoinToken("", "pull-only"),
	} {
		_, _, err := decodeJoinToken(token)
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
