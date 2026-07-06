package cluster

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestParseJoinToken(t *testing.T) {
	id := uuid.New()
	secret := strings.Repeat("s", 43)
	fp := strings.Repeat("a", 32)
	token := id.String() + "." + secret + "." + fp

	gotID, gotSecret, gotFP, err := ParseJoinToken(" " + token + "\n")
	require.NoError(t, err)
	require.Equal(t, id, gotID)
	require.Equal(t, secret, gotSecret)
	require.Equal(t, fp, gotFP)
}

func TestParseJoinTokenRejectsMalformed(t *testing.T) {
	id := uuid.New().String()
	for _, tok := range []string{
		"",
		"one.two",
		"not-a-uuid." + strings.Repeat("s", 43) + "." + strings.Repeat("a", 32),
		id + ".." + strings.Repeat("a", 32),                  // empty secret
		id + "." + strings.Repeat("s", 43) + ".shortfp",      // wrong fp length
		id + "." + strings.Repeat("s", 43) + ".a." + "extra", // too many parts
	} {
		_, _, _, err := ParseJoinToken(tok)
		require.ErrorIs(t, err, ErrInvalidToken, "token %q", tok)
	}
}

func TestValidateRoles(t *testing.T) {
	require.NoError(t, ValidateRoles([]string{"worker", "edge"}, AssignableRoles))
	require.ErrorIs(t, ValidateRoles(nil, AssignableRoles), ErrInvalidRole)
	require.ErrorIs(t, ValidateRoles([]string{"master"}, AssignableRoles), ErrInvalidRole)
	require.ErrorIs(t, ValidateRoles([]string{"worker", "bogus"}, AllRoles), ErrInvalidRole)
	require.NoError(t, ValidateRoles([]string{"master", "worker"}, AllRoles))
}
