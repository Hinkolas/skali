package auth

import "github.com/Hinkolas/skali/internal/crypt"

// cryptVersion mirrors the internal/crypt ciphertext version byte; kept here
// so existing tests can assert the wire format.
const cryptVersion = 0x01

// deriveKey turns the operator's AUTH_SECRET into the AES-256 key used for
// values the auth system encrypts at rest (TOTP secrets). The info string is
// pinned: existing ciphertexts must keep decrypting. Rotating AUTH_SECRET
// invalidates those values — users must re-enroll 2FA.
func deriveKey(secret string) ([]byte, error) {
	return crypt.Key(secret, "skali/auth/totp-key/v1")
}

func encrypt(key, plaintext []byte) ([]byte, error) {
	return crypt.Encrypt(key, plaintext)
}

func decrypt(key, ciphertext []byte) ([]byte, error) {
	return crypt.Decrypt(key, ciphertext)
}
