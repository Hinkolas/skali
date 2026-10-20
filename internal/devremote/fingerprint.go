package devremote

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

// fingerprintPrefix names the digest in the --ca form.
const fingerprintPrefix = "sha256:"

// Fingerprint renders the SHA-256 of a DER certificate in the form --ca
// takes: sha256: and 64 lowercase hex digits. It covers the whole
// certificate, which is what lands in a trust store, and matches the
// SHA-256 fingerprint Keychain Access, Firefox, and openssl show.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return fingerprintPrefix + hex.EncodeToString(sum[:])
}

// FingerprintFromHex renders a hex SHA-256 (as localdev.CA carries it) in
// the --ca form.
func FingerprintFromHex(digest string) string {
	return fingerprintPrefix + strings.ToLower(digest)
}

// ParseFingerprint normalizes a fingerprint to the --ca form. Besides that
// form it accepts the spellings people copy from elsewhere: uppercase,
// colon- or space-separated bytes, and openssl's "SHA256 Fingerprint="
// prefix.
func ParseFingerprint(value string) (string, error) {
	digest := strings.TrimSpace(value)
	lower := strings.ToLower(digest)
	for _, prefix := range []string{"sha256 fingerprint=", "sha256:", "sha-256:"} {
		if strings.HasPrefix(lower, prefix) {
			digest = digest[len(prefix):]
			break
		}
	}
	digest = strings.NewReplacer(":", "", " ", "").Replace(strings.ToLower(digest))
	if len(digest) != sha256.Size*2 {
		return "", errors.New("the CA fingerprint must be a SHA-256 digest like sha256:4be1…, " +
			"as skali dev status prints it on the remote machine")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return "", errors.New("the CA fingerprint is not hexadecimal; copy it again from skali dev status on the remote machine")
	}
	return fingerprintPrefix + digest, nil
}
