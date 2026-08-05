package auth

import (
	"crypto/sha256"
	"fmt"

	"github.com/Hinkolas/skali/internal/utils"
)

// newToken returns a fresh opaque bearer token: 32 bytes of entropy,
// URL-safe base64 (43 chars). The plaintext is returned to the client exactly
// once; the database only ever sees its hash.
func newToken() (string, error) {
	token, err := utils.RandomToken(32)
	if err != nil {
		return "", fmt.Errorf("auth: token: %w", err)
	}
	return token, nil
}

// hashToken maps a token to its storage form. Plain sha256 is sufficient here
// (unlike backup codes) because tokens carry 256 bits of entropy.
func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
