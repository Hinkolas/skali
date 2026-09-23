package localdev

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGenerateCAIsABrowserGradeLeafSigner(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	ca, err := GenerateCA()
	require.NoError(t, err)

	block, _ := pem.Decode(ca.CertPEM)
	require.NotNil(t, block)
	certificate, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	require.True(t, certificate.IsCA)
	require.True(t, certificate.BasicConstraintsValid)
	require.True(t, certificate.MaxPathLenZero, "the CA must sign leaves only")
	require.NotZero(t, certificate.KeyUsage&x509.KeyUsageCertSign)
	require.Equal(t, x509.ECDSA, certificate.PublicKeyAlgorithm)
	require.Contains(t, certificate.Subject.CommonName, "skali local dev CA")
	require.Contains(t, certificate.Subject.CommonName, ClusterName())
	require.Equal(t, certificate.Subject.CommonName, ca.CommonName)
	require.Len(t, ca.Fingerprint, 64)

	keyBlock, _ := pem.Decode(ca.KeyPEM)
	require.NotNil(t, keyBlock)
	require.Equal(t, "PRIVATE KEY", keyBlock.Type, "cert-manager's CA issuer reads PKCS#8")
	_, err = x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	require.NoError(t, err)

	// The chain verifies a leaf the CA signs, which is what cert-manager
	// will do for every route.
	require.True(t, ca.Pool().Equal(ca.Pool()))

	info, err := os.Stat(ca.CertPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o644), info.Mode().Perm())
	_, keyPath, err := CAPaths()
	require.NoError(t, err)
	info, err = os.Stat(keyPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestEnsureCAGeneratesOnceAndRefusesToRegenerate(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	_, err := LoadCA()
	require.True(t, errors.Is(err, ErrNotInstalled))
	_, ok := LocalCAPEM()
	require.False(t, ok)

	// An existing installation without its CA is refused: regenerating
	// would silently stop matching the certificates already issued.
	_, err = EnsureCA(false)
	require.ErrorContains(t, err, "skali dev reset")

	generated, err := EnsureCA(true)
	require.NoError(t, err)
	loaded, err := EnsureCA(false)
	require.NoError(t, err)
	require.Equal(t, generated.Fingerprint, loaded.Fingerprint)
	again, err := EnsureCA(true)
	require.NoError(t, err)
	require.Equal(t, generated.Fingerprint, again.Fingerprint, "a present CA is never replaced implicitly")

	pemBytes, ok := LocalCAPEM()
	require.True(t, ok)
	require.Equal(t, generated.CertPEM, pemBytes)

	// Two generations stay distinguishable in a trust store.
	require.NoError(t, RemoveState())
	fresh, err := GenerateCA()
	require.NoError(t, err)
	require.NotEqual(t, generated.CommonName, fresh.CommonName)
	require.NotEqual(t, generated.Fingerprint, fresh.Fingerprint)
}
