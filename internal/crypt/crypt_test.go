package crypt

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key, err := Key("some-operator-secret-at-least-32-chars", "skali/test/v1")
	require.NoError(t, err)

	ct, err := Encrypt(key, []byte("JBSWY3DPEHPK3PXP"))
	require.NoError(t, err)
	require.Equal(t, byte(version), ct[0])

	pt, err := Decrypt(key, ct)
	require.NoError(t, err)
	require.Equal(t, "JBSWY3DPEHPK3PXP", string(pt))
}

func TestDecryptRejectsWrongKey(t *testing.T) {
	key1, err := Key("secret-one-that-is-long-enough-xxxxx", "skali/test/v1")
	require.NoError(t, err)
	key2, err := Key("secret-two-that-is-long-enough-xxxxx", "skali/test/v1")
	require.NoError(t, err)

	ct, err := Encrypt(key1, []byte("payload"))
	require.NoError(t, err)

	_, err = Decrypt(key2, ct)
	require.Error(t, err)
}

func TestInfoStringSeparatesDomains(t *testing.T) {
	// The same secret with different info strings must yield different keys,
	// so ciphertexts sealed for one domain never decrypt in another.
	keyA, err := Key("same-secret-material-goes-in-here-ok", "skali/auth/totp-key/v1")
	require.NoError(t, err)
	keyB, err := Key("same-secret-material-goes-in-here-ok", "skali/cluster/ca-key/v1")
	require.NoError(t, err)
	require.NotEqual(t, keyA, keyB)

	ct, err := Encrypt(keyA, []byte("payload"))
	require.NoError(t, err)
	_, err = Decrypt(keyB, ct)
	require.Error(t, err)
}

func TestDecryptRejectsGarbage(t *testing.T) {
	key, err := Key("some-operator-secret-at-least-32-chars", "skali/test/v1")
	require.NoError(t, err)

	for _, ct := range [][]byte{nil, {}, {0x02}, {version, 1, 2, 3}} {
		_, err := Decrypt(key, ct)
		require.Error(t, err)
	}
}

func TestKeyDeterministic(t *testing.T) {
	a, err := Key("same-secret-material-goes-in-here-ok", "skali/test/v1")
	require.NoError(t, err)
	b, err := Key("same-secret-material-goes-in-here-ok", "skali/test/v1")
	require.NoError(t, err)
	require.Equal(t, a, b)
	require.Len(t, a, 32)
}
