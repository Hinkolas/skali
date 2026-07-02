package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// newToken returns a fresh opaque bearer token: 32 bytes of entropy,
// URL-safe base64 (43 chars). The plaintext is returned to the client exactly
// once; the database only ever sees its hash.
func newToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("auth: token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// hashToken maps a token to its storage form. Plain sha256 is sufficient here
// (unlike backup codes) because tokens carry 256 bits of entropy.
func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
