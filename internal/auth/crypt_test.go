package auth

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key, err := deriveKey("some-operator-secret-at-least-32-chars")
	require.NoError(t, err)

	ct, err := encrypt(key, []byte("JBSWY3DPEHPK3PXP"))
	require.NoError(t, err)
	require.Equal(t, byte(cryptVersion), ct[0])

	pt, err := decrypt(key, ct)
	require.NoError(t, err)
	require.Equal(t, "JBSWY3DPEHPK3PXP", string(pt))
}

func TestDecryptRejectsWrongKey(t *testing.T) {
	key1, err := deriveKey("secret-one-that-is-long-enough-xxxxx")
	require.NoError(t, err)
	key2, err := deriveKey("secret-two-that-is-long-enough-xxxxx")
	require.NoError(t, err)

	ct, err := encrypt(key1, []byte("payload"))
	require.NoError(t, err)

	_, err = decrypt(key2, ct)
	require.Error(t, err)
}

func TestDecryptRejectsGarbage(t *testing.T) {
	key, err := deriveKey("some-operator-secret-at-least-32-chars")
	require.NoError(t, err)

	for _, ct := range [][]byte{nil, {}, {0x02}, {cryptVersion, 1, 2, 3}} {
		_, err := decrypt(key, ct)
		require.Error(t, err)
	}
}

func TestDeriveKeyDeterministic(t *testing.T) {
	a, err := deriveKey("same-secret-material-goes-in-here-ok")
	require.NoError(t, err)
	b, err := deriveKey("same-secret-material-goes-in-here-ok")
	require.NoError(t, err)
	require.Equal(t, a, b)
	require.Len(t, a, 32)
}
