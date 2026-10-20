package devremote

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFingerprint(t *testing.T) {
	t.Parallel()
	der := []byte("certificate bytes")
	sum := sha256.Sum256(der)
	want := "sha256:" + hex.EncodeToString(sum[:])
	require.Equal(t, want, Fingerprint(der))
	require.Equal(t, want, FingerprintFromHex(strings.ToUpper(hex.EncodeToString(sum[:]))))
}

func TestParseFingerprint(t *testing.T) {
	t.Parallel()
	digest := strings.Repeat("4b", 32)
	want := "sha256:" + digest
	colons := strings.ToUpper(strings.Join(splitPairs(digest), ":"))
	spaces := strings.ToUpper(strings.Join(splitPairs(digest), " "))
	for _, input := range []string{
		want,
		digest,
		"SHA256:" + strings.ToUpper(digest),
		"  " + want + "\n",
		"SHA256 Fingerprint=" + colons,
		spaces,
		"sha-256:" + digest,
	} {
		got, err := ParseFingerprint(input)
		require.NoError(t, err, input)
		require.Equal(t, want, got, input)
	}
	for _, input := range []string{
		"",
		"sha256:",
		"sha256:" + digest[:62],
		"sha256:" + digest + "00",
		"sha256:" + strings.Repeat("zz", 32),
		"sha1:" + digest,
	} {
		_, err := ParseFingerprint(input)
		require.Error(t, err, input)
	}
}

func splitPairs(hexDigest string) []string {
	var pairs []string
	for index := 0; index < len(hexDigest); index += 2 {
		pairs = append(pairs, hexDigest[index:index+2])
	}
	return pairs
}
