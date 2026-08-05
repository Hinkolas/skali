package utils

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// RandomToken returns byteLen bytes of entropy as unpadded URL-safe
// base64 (43 characters for 32 bytes). Callers wrap the error with
// their own context.
func RandomToken(byteLen int) (string, error) {
	raw := make([]byte, byteLen)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
