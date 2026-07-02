package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
)

// cryptVersion prefixes every ciphertext so the key derivation or cipher can
// change later without a schema migration.
const cryptVersion = 0x01

// deriveKey turns the operator's AUTH_SECRET into the AES-256 key used for
// values encrypted at rest (TOTP secrets). Rotating AUTH_SECRET therefore
// invalidates those values — users must re-enroll 2FA.
func deriveKey(secret string) ([]byte, error) {
	return hkdf.Key(sha256.New, []byte(secret), nil, "skali/auth/totp-key/v1", 32)
}

// encrypt seals plaintext as: version byte || 12-byte nonce || AES-256-GCM
// ciphertext. Random nonces are safe at this call rate (one encryption per 2FA
// enrollment, nowhere near the 2^32 birthday bound).
func encrypt(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 0, 1+len(nonce)+len(plaintext)+gcm.Overhead())
	out = append(out, cryptVersion)
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, plaintext, nil), nil
}

func decrypt(key, ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < 1 || ciphertext[0] != cryptVersion {
		return nil, fmt.Errorf("auth: unknown ciphertext version")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	rest := ciphertext[1:]
	if len(rest) < gcm.NonceSize() {
		return nil, fmt.Errorf("auth: ciphertext too short")
	}
	return gcm.Open(nil, rest[:gcm.NonceSize()], rest[gcm.NonceSize():], nil)
}
