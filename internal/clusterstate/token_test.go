package clusterstate

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Hinkolas/skali/internal/utils"
	"github.com/stretchr/testify/require"
)

func TestEnrollmentTokenCarriesAuthenticationOnly(t *testing.T) {
	pin := "sha256:" + strings.Repeat("a", 64)
	encoded, token, err := NewToken("invite-1", pin)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(encoded, "skali."))

	body, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(encoded, TokenPrefix))
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload))
	require.ElementsMatch(t, []string{"version", "invitation", "credential", "caPin"},
		utils.SortedKeys(payload))
	for _, forbidden := range []string{
		"server", "cluster", "role", "capabilities", "registryCredential", "k3sToken",
	} {
		require.NotContains(t, payload, forbidden)
	}

	parsed, err := ParseToken(encoded)
	require.NoError(t, err)
	require.Equal(t, token, parsed)
	require.True(t, MatchCredential(CredentialHash(token.Credential), token.Credential))
	require.False(t, MatchCredential(CredentialHash(token.Credential), "different"))
}

func TestEnrollmentTokenRejectsUnknownVersion(t *testing.T) {
	payload, err := json.Marshal(Token{
		Version: 99, Invitation: "i", Credential: "YQ",
		CAPin: "sha256:" + strings.Repeat("a", 64),
	})
	require.NoError(t, err)
	_, err = ParseToken(TokenPrefix + base64.RawURLEncoding.EncodeToString(payload))
	require.ErrorContains(t, err, "unsupported enrollment token version")
}

func TestEnrollmentTokenRejectsMalformedCAPin(t *testing.T) {
	for _, pin := range []string{
		"md5:" + strings.Repeat("a", 32),
		"sha256:short",
		"sha256:" + strings.Repeat("z", 64),
	} {
		payload, err := json.Marshal(Token{
			Version: CurrentVersion, Invitation: "i", Credential: "YQ", CAPin: pin,
		})
		require.NoError(t, err)
		_, err = ParseToken(TokenPrefix + base64.RawURLEncoding.EncodeToString(payload))
		require.ErrorContains(t, err, "coordinator CA pin", pin)
	}
}

func TestTokenEndpointHintsRemainOptionalAndNormalizeWrappedInput(t *testing.T) {
	token, original, err := NewToken("invitation", "sha256:"+strings.Repeat("a", 64))
	require.NoError(t, err)
	hinted, err := WithCoordinators(token, []string{"10.10.1.1", "https://10.10.2.1:6444"})
	require.NoError(t, err)
	parsed, err := ParseToken(hinted[:22] + "\r\n  " + hinted[22:])
	require.NoError(t, err)
	require.Equal(t, original.Credential, parsed.Credential)
	require.Equal(t, original.CAPin, parsed.CAPin)
	require.Equal(t, []string{"https://10.10.1.1:6444", "https://10.10.2.1:6444"}, parsed.Coordinators)
	old, err := ParseToken(token)
	require.NoError(t, err)
	require.Empty(t, old.Coordinators)
}
