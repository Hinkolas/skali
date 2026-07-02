package auth

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewToken(t *testing.T) {
	a, err := newToken()
	require.NoError(t, err)
	b, err := newToken()
	require.NoError(t, err)

	require.Len(t, a, 43) // 32 bytes, unpadded base64url
	require.NotEqual(t, a, b)
}

func TestHashTokenDeterministic(t *testing.T) {
	require.Equal(t, hashToken("abc"), hashToken("abc"))
	require.NotEqual(t, hashToken("abc"), hashToken("abd"))
	require.Len(t, hashToken("abc"), 32)
}
