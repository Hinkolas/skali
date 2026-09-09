package registrytoken

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// pinnedKeyPEM and pinnedKeyID pin the libtrust key ID derivation: the
// registry indexes its trusted keys by exactly this string, so any drift
// here would make every minted token unverifiable. This is a publicly known
// test key; never use it to sign tokens for an actual registry.
const (
	pinnedKeyPEM = `-----BEGIN EC PRIVATE KEY-----
MHcCAQEEIPEHnHCc0EcOYzY6FEB8/qOJWiZPUJUDLg2ymun3du4XoAoGCCqGSM49
AwEHoUQDQgAEZprVjE6IElO10HHFdqXFvUYvbV0PFaadLpDmlDwhvuN0Q2zcMdt8
UYk8Fu5SqO9ZSpaEhhngdg1sR6vwkq8SuA==
-----END EC PRIVATE KEY-----
`
	pinnedKeyID = "V75E:Q5EP:6TF4:KN6Z:3TQM:OPLK:SJDB:7HMA:DOHV:ZOS6:YHLC:LSVG"
)

func TestGenerateSigningKeypair(t *testing.T) {
	keyPEM, certPEM, err := GenerateSigningKeypair()
	require.NoError(t, err)

	signer, err := LoadSigner(keyPEM)
	require.NoError(t, err)

	block, _ := pem.Decode(certPEM)
	require.NotNil(t, block)
	require.Equal(t, "CERTIFICATE", block.Type)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	require.Equal(t, "skalid registry token signer", cert.Subject.CommonName)
	require.True(t, cert.NotAfter.After(time.Now().AddDate(99, 0, 0)))

	// The certificate carries the same public key the signer holds, so the
	// registry derives the same key ID from its rootcertbundle.
	certKey, ok := cert.PublicKey.(*ecdsa.PublicKey)
	require.True(t, ok)
	certKeyID, err := deriveKeyID(certKey)
	require.NoError(t, err)
	require.Equal(t, signer.KeyID(), certKeyID)
}

func TestKeyIDPinned(t *testing.T) {
	signer, err := LoadSigner([]byte(pinnedKeyPEM))
	require.NoError(t, err)
	require.Equal(t, pinnedKeyID, signer.KeyID())
	require.Regexp(t, regexp.MustCompile(`^([A-Z2-7]{4}:){11}[A-Z2-7]{4}$`), signer.KeyID())
}

func TestLoadSignerRejectsGarbage(t *testing.T) {
	_, err := LoadSigner([]byte("not a key"))
	require.Error(t, err)
	_, err = LoadSigner(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte("junk")}))
	require.Error(t, err)
}

func TestMint(t *testing.T) {
	signer, err := LoadSigner([]byte(pinnedKeyPEM))
	require.NoError(t, err)
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	access := []Access{{Type: "repository", Name: "skali/demo/web", Actions: []string{"pull", "push"}}}

	token, err := signer.Mint(Service, "admin@example.com", access, now, TokenTTL)
	require.NoError(t, err)
	parts := strings.Split(token, ".")
	require.Len(t, parts, 3)

	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	require.NoError(t, err)
	var header map[string]string
	require.NoError(t, json.Unmarshal(headerJSON, &header))
	require.Equal(t, "ES256", header["alg"])
	require.Equal(t, "JWT", header["typ"])
	require.Equal(t, pinnedKeyID, header["kid"])

	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var decoded claims
	require.NoError(t, json.Unmarshal(claimsJSON, &decoded))
	require.Equal(t, Issuer, decoded.Issuer)
	require.Equal(t, "admin@example.com", decoded.Subject)
	require.Equal(t, Service, decoded.Audience)
	require.Equal(t, now.Unix(), decoded.IssuedAt)
	require.Equal(t, now.Add(TokenTTL).Unix(), decoded.ExpiresAt)
	require.Equal(t, now.Add(-time.Minute).Unix(), decoded.NotBefore)
	require.Len(t, decoded.JWTID, 32)
	require.Equal(t, access, decoded.Access)

	// Verify the raw R||S signature against the signing input.
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	require.NoError(t, err)
	require.Len(t, signature, 64)
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(signature[:32])
	s := new(big.Int).SetBytes(signature[32:])
	require.True(t, ecdsa.Verify(&signer.key.PublicKey, digest[:], r, s))
}

func TestMintEmptyAccess(t *testing.T) {
	signer, err := LoadSigner([]byte(pinnedKeyPEM))
	require.NoError(t, err)
	token, err := signer.Mint(Service, "admin@example.com", nil, time.Now(), TokenTTL)
	require.NoError(t, err)
	claimsJSON, err := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[1])
	require.NoError(t, err)
	// A login probe token must carry an empty access array, never null.
	require.Contains(t, string(claimsJSON), `"access":[]`)
}
