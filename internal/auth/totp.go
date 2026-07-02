package auth

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// TOTP parameters: the compatibility baseline every authenticator app
// implements (RFC 6238 defaults).
const (
	totpPeriod = 30
	totpDigits = otp.DigitsSix
)

const totpIssuer = "skali"

// generateTOTP creates a new secret for email and returns the key, whose
// Secret() feeds the encrypted column and URL() the client's QR code.
func generateTOTP(email string) (*otp.Key, error) {
	return totp.Generate(totp.GenerateOpts{
		Issuer:      totpIssuer,
		AccountName: email,
		Period:      totpPeriod,
		Digits:      totpDigits,
		Algorithm:   otp.AlgorithmSHA1,
	})
}

// matchTOTPStep reports which time step (unix/30) the code is valid for, or
// ok=false. Steps within ±1 of now are checked one at a time — the
// library's own Skew option cannot tell the caller which step matched, and the
// matched step is what the replay guard persists.
func matchTOTPStep(secret, code string, now time.Time) (step int64, ok bool) {
	current := now.Unix() / totpPeriod
	for _, delta := range []int64{0, -1, 1} {
		step := current + delta
		valid, err := totp.ValidateCustom(code, secret, time.Unix(step*totpPeriod, 0), totp.ValidateOpts{
			Period:    totpPeriod,
			Skew:      0,
			Digits:    totpDigits,
			Algorithm: otp.AlgorithmSHA1,
		})
		if err == nil && valid {
			return step, true
		}
	}
	return 0, false
}

// Backup-code alphabet: lowercase base32-ish with the ambiguous characters
// (0/o, 1/i/l) removed, since users read these off paper.
const backupCodeAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

const (
	backupCodeCount = 10
	backupCodeLen   = 10 // ~49.5 bits over the 31-char alphabet
)

// generateBackupCodes returns codes in display form ("xxxxx-xxxxx").
func generateBackupCodes() ([]string, error) {
	max := big.NewInt(int64(len(backupCodeAlphabet)))
	codes := make([]string, backupCodeCount)
	for i := range codes {
		var b strings.Builder
		for range backupCodeLen {
			n, err := rand.Int(rand.Reader, max)
			if err != nil {
				return nil, fmt.Errorf("auth: backup code: %w", err)
			}
			b.WriteByte(backupCodeAlphabet[n.Int64()])
		}
		code := b.String()
		codes[i] = code[:5] + "-" + code[5:]
	}
	return codes, nil
}

// normalizeBackupCode maps user input to the canonical hashed form: lowercase,
// separators stripped. Hash and verify only ever see this form.
func normalizeBackupCode(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	return strings.NewReplacer("-", "", " ", "").Replace(code)
}
