package auth

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHashAndVerifyPassword(t *testing.T) {
	phc, err := hashPassword("correct horse battery staple")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(phc, "$argon2id$v=19$m=65536,t=3,p=4$"))

	require.True(t, verifyPassword("correct horse battery staple", phc))
	require.False(t, verifyPassword("wrong password", phc))
	require.False(t, verifyPassword("", phc))
}

func TestHashPasswordUniqueSalts(t *testing.T) {
	a, err := hashPassword("same input")
	require.NoError(t, err)
	b, err := hashPassword("same input")
	require.NoError(t, err)
	require.NotEqual(t, a, b)
}

func TestVerifyPasswordMalformedHash(t *testing.T) {
	for _, phc := range []string{
		"",
		"not a hash",
		"$argon2i$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA",  // wrong variant
		"$argon2id$v=18$m=65536,t=3,p=4$c2FsdA$aGFzaA", // wrong version
		"$argon2id$v=19$m=65536,t=3,p=4$!!!$aGFzaA",    // bad base64 salt
		"$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$!!!",    // bad base64 key
	} {
		require.False(t, verifyPassword("anything", phc), "phc=%q", phc)
	}
}

func TestDummyHashVerifies(t *testing.T) {
	// The enumeration guard must exercise the real argon2 path.
	require.False(t, verifyPassword("anything", dummyHash()))
	require.True(t, verifyPassword("skali-dummy-password-timing-equalizer", dummyHash()))
}
