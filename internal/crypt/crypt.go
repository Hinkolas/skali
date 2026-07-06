// Package crypt provides the at-rest encryption primitives shared by skali's
// feature packages: AES-256-GCM with keys derived from the operator's
// AUTH_SECRET via HKDF. Each caller passes its own HKDF info string, so
// ciphertexts are domain-separated — a key derived for one purpose can never
// decrypt values sealed for another.
package crypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
)

// version prefixes every ciphertext so the key derivation or cipher can
// change later without a schema migration.
const version = 0x01

// Key derives a 32-byte AES-256 key from the operator's secret. The info
// string namespaces the derivation (e.g. "skali/auth/totp-key/v1"); rotating
// the secret invalidates every value sealed under it.
func Key(secret, info string) ([]byte, error) {
	return hkdf.Key(sha256.New, []byte(secret), nil, info, 32)
}

// Encrypt seals plaintext as: version byte || 12-byte nonce || AES-256-GCM
// ciphertext. Random nonces are safe at skali's call rates (rare, per-secret
// writes — nowhere near the 2^32 birthday bound).
func Encrypt(key, plaintext []byte) ([]byte, error) {
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
	out = append(out, version)
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, plaintext, nil), nil
}

// Decrypt reverses Encrypt with the same key.
func Decrypt(key, ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < 1 || ciphertext[0] != version {
		return nil, fmt.Errorf("crypt: unknown ciphertext version")
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
		return nil, fmt.Errorf("crypt: ciphertext too short")
	}
	return gcm.Open(nil, rest[:gcm.NonceSize()], rest[gcm.NonceSize():], nil)
}
