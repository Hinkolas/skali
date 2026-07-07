package cluster

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func testCSR(t *testing.T) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), key
}

func TestSignNodeCertRoundTrip(t *testing.T) {
	ca, err := generateCA()
	require.NoError(t, err)

	nodeID, err := uuid.NewV7()
	require.NoError(t, err)
	csrPEM, key := testCSR(t)

	certPEM, serialHex, err := ca.SignNodeCert(csrPEM, nodeID)
	require.NoError(t, err)
	require.NotEmpty(t, serialHex)

	cert, err := parseCertPEM(certPEM)
	require.NoError(t, err)

	// Identity: UUID as CN and DNS SAN; the CSR's public key is honored.
	require.Equal(t, nodeID.String(), cert.Subject.CommonName)
	require.Equal(t, []string{nodeID.String()}, cert.DNSNames)
	require.True(t, key.PublicKey.Equal(cert.PublicKey))
	require.Equal(t, serialHex, cert.SerialNumber.Text(16))

	// Chains to the CA with both server and client EKU (verified separately —
	// x509 verification requires each requested EKU to be present).
	for _, eku := range []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth} {
		_, err = cert.Verify(x509.VerifyOptions{
			Roots:     ca.Pool(),
			DNSName:   nodeID.String(),
			KeyUsages: []x509.ExtKeyUsage{eku},
		})
		require.NoError(t, err)
	}

	// A different CA must reject it.
	other, err := generateCA()
	require.NoError(t, err)
	_, err = cert.Verify(x509.VerifyOptions{Roots: other.Pool()})
	require.Error(t, err)
}

func TestSignNodeCertRejectsBadCSR(t *testing.T) {
	ca, err := generateCA()
	require.NoError(t, err)
	nodeID := uuid.New()

	_, _, err = ca.SignNodeCert([]byte("not a csr"), nodeID)
	require.Error(t, err)

	// Corrupt the CSR body: parse or signature check must fail.
	csrPEM, _ := testCSR(t)
	block, _ := pem.Decode(csrPEM)
	block.Bytes[len(block.Bytes)-10] ^= 0xff
	_, _, err = ca.SignNodeCert(pem.EncodeToMemory(block), nodeID)
	require.Error(t, err)
}

func TestIssueServerCert(t *testing.T) {
	ca, err := generateCA()
	require.NoError(t, err)

	cert, err := ca.IssueServerCert([]string{"10.0.0.1", "master.example.com", ""})
	require.NoError(t, err)

	// Chain is [leaf, CA] so pinning clients can see the CA.
	require.Len(t, cert.Certificate, 2)
	require.Equal(t, ca.Cert.Raw, cert.Certificate[1])
	require.Equal(t, masterCN, cert.Leaf.Subject.CommonName)
	require.Len(t, cert.Leaf.IPAddresses, 1)
	require.Equal(t, []string{"master.example.com"}, cert.Leaf.DNSNames)
}

func TestIssueClientCert(t *testing.T) {
	ca, err := generateCA()
	require.NoError(t, err)

	cert, err := ca.IssueClientCert()
	require.NoError(t, err)
	require.Equal(t, masterCN, cert.Leaf.Subject.CommonName)

	_, err = cert.Leaf.Verify(x509.VerifyOptions{
		Roots:     ca.Pool(),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	require.NoError(t, err)
}

// TestIssuePEM pins the file-based identities the mirror consumes: parseable
// PEM pairs that chain to the CA with the right EKUs and SANs.
func TestIssuePEM(t *testing.T) {
	ca, err := generateCA()
	require.NoError(t, err)

	certPEM, keyPEM, err := ca.ServerPEM("skali-registry", []string{"10.0.0.1", "localhost"}, certLifetime)
	require.NoError(t, err)
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	require.NoError(t, err)
	require.Equal(t, "skali-registry", leaf.Subject.CommonName)
	require.Len(t, leaf.IPAddresses, 1)
	require.Equal(t, []string{"localhost"}, leaf.DNSNames)
	_, err = leaf.Verify(x509.VerifyOptions{
		Roots:     ca.Pool(),
		DNSName:   "localhost",
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	require.NoError(t, err)

	certPEM, keyPEM, err = ca.ClientPEM()
	require.NoError(t, err)
	pair, err = tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)
	leaf, err = x509.ParseCertificate(pair.Certificate[0])
	require.NoError(t, err)
	require.Equal(t, masterCN, leaf.Subject.CommonName)
	_, err = leaf.Verify(x509.VerifyOptions{
		Roots:     ca.Pool(),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	require.NoError(t, err)

	require.Equal(t, ca.CertPEM, ca.CAPEM())
}

func TestFingerprintStable(t *testing.T) {
	ca, err := generateCA()
	require.NoError(t, err)

	fp := Fingerprint(ca.Cert)
	require.Len(t, fp, 32)
	require.Equal(t, fp, Fingerprint(ca.Cert))

	other, err := generateCA()
	require.NoError(t, err)
	require.NotEqual(t, fp, Fingerprint(other.Cert))
}
