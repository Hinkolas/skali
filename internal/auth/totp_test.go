package auth

import (
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"
)

func TestMatchTOTPStep(t *testing.T) {
	key, err := generateTOTP("nick@example.com")
	require.NoError(t, err)
	secret := key.Secret()

	now := time.Unix(1_900_000_000, 0)
	code, err := totp.GenerateCode(secret, now)
	require.NoError(t, err)

	step, ok := matchTOTPStep(secret, code, now)
	require.True(t, ok)
	require.Equal(t, now.Unix()/totpPeriod, step)

	// The same code is accepted one step later (skew) and maps to the step it
	// was generated for — which is what the replay guard stores.
	stepLater, ok := matchTOTPStep(secret, code, now.Add(totpPeriod*time.Second))
	require.True(t, ok)
	require.Equal(t, step, stepLater)

	// Two steps away is outside the skew window.
	_, ok = matchTOTPStep(secret, code, now.Add(2*totpPeriod*time.Second))
	require.False(t, ok)

	_, ok = matchTOTPStep(secret, "000000", now)
	require.False(t, ok)
}

func TestGenerateTOTPURI(t *testing.T) {
	key, err := generateTOTP("nick@example.com")
	require.NoError(t, err)
	require.Contains(t, key.URL(), "otpauth://totp/")
	require.Contains(t, key.URL(), "issuer=skali")
	require.NotEmpty(t, key.Secret())
}

func TestGenerateBackupCodes(t *testing.T) {
	codes, err := generateBackupCodes()
	require.NoError(t, err)
	require.Len(t, codes, backupCodeCount)

	seen := map[string]bool{}
	for _, c := range codes {
		require.Regexp(t, `^[a-z2-9]{5}-[a-z2-9]{5}$`, c)
		require.False(t, seen[c], "duplicate code %s", c)
		seen[c] = true
	}
}

func TestNormalizeBackupCode(t *testing.T) {
	require.Equal(t, "abcdefghjk", normalizeBackupCode("ABCDE-FGHJK"))
	require.Equal(t, "abcdefghjk", normalizeBackupCode("  abcde fghjk "))
	require.Equal(t, "abcdefghjk", normalizeBackupCode("abcdefghjk"))
}
