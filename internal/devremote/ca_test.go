package devremote

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/api"
	"github.com/Hinkolas/skali/internal/bundle"
)

type testCA struct {
	certificate *x509.Certificate
	key         *ecdsa.PrivateKey
	pem         []byte
}

func newTestCA(t *testing.T, name string) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	certificate, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return &testCA{certificate: certificate, key: key,
		pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// leaf issues a server certificate for the given names, signed by ca.
func (ca *testCA) leaf(t *testing.T, names ...string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: names[0]},
		DNSNames:     names,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.certificate, &key.PublicKey, ca.key)
	require.NoError(t, err)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// edge starts a TLS server presenting leaf and answering the CA path with
// status and body.
func edge(t *testing.T, leaf tls.Certificate, status int, body []byte) string {
	t.Helper()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != caURLPath || r.Host != bundle.LocalPlatformHost {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{leaf}}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server.Listener.Addr().String()
}

func fetch(address, fingerprint string) (*RemoteCA, error) {
	var dialer net.Dialer
	return fetchCA(context.Background(), address, fingerprint, dialer.DialContext)
}

func TestCAURLPathMatchesTheAPI(t *testing.T) {
	t.Parallel()
	require.Equal(t, "/api"+api.DevCAPath, caURLPath)
}

func TestFetchCAAcceptsThePinnedCA(t *testing.T) {
	t.Parallel()
	ca := newTestCA(t, "skali local dev CA (test)")
	address := edge(t, ca.leaf(t, bundle.LocalPlatformHost), http.StatusOK, ca.pem)
	got, err := fetch(address, Fingerprint(ca.certificate.Raw))
	require.NoError(t, err)
	require.Equal(t, ca.pem, got.PEM)
	require.Equal(t, "skali local dev CA (test)", got.CommonName())
	require.Equal(t, Fingerprint(ca.certificate.Raw), got.Fingerprint)
}

// A mismatch names neither fingerprint: printing the one that was seen
// would invite pasting it back.
func TestFetchCARefusesAnotherCA(t *testing.T) {
	t.Parallel()
	served := newTestCA(t, "served")
	pinned := newTestCA(t, "pinned")
	address := edge(t, served.leaf(t, bundle.LocalPlatformHost), http.StatusOK, served.pem)
	_, err := fetch(address, Fingerprint(pinned.certificate.Raw))
	require.ErrorIs(t, err, ErrFingerprintMismatch)
	require.NotContains(t, err.Error(), Fingerprint(served.certificate.Raw)[len("sha256:"):][:16])
}

// Serving a copy of the pinned CA is not enough: the edge must present a
// certificate that CA issued, for the platform host.
func TestFetchCARequiresTheEdgeChainToThePinnedCA(t *testing.T) {
	t.Parallel()
	pinned := newTestCA(t, "pinned")
	impostor := newTestCA(t, "impostor")
	address := edge(t, impostor.leaf(t, bundle.LocalPlatformHost), http.StatusOK, pinned.pem)
	_, err := fetch(address, Fingerprint(pinned.certificate.Raw))
	require.ErrorContains(t, err, "did not issue")

	wrongHost := edge(t, pinned.leaf(t, "elsewhere.localhost"), http.StatusOK, pinned.pem)
	_, err = fetch(wrongHost, Fingerprint(pinned.certificate.Raw))
	require.ErrorContains(t, err, "did not issue")
}

func TestFetchCARefusesALeafCertificate(t *testing.T) {
	t.Parallel()
	ca := newTestCA(t, "ca")
	leaf := ca.leaf(t, bundle.LocalPlatformHost)
	leafPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Certificate[0]})
	address := edge(t, leaf, http.StatusOK, leafPEM)
	_, err := fetch(address, Fingerprint(leaf.Certificate[0]))
	require.ErrorContains(t, err, "not a CA certificate")
}

func TestFetchCAExplainsAnEdgeWithoutTheEndpoint(t *testing.T) {
	t.Parallel()
	ca := newTestCA(t, "ca")
	fingerprint := Fingerprint(ca.certificate.Raw)

	missing := edge(t, ca.leaf(t, bundle.LocalPlatformHost), http.StatusNotFound, nil)
	_, err := fetch(missing, fingerprint)
	require.ErrorIs(t, err, ErrCAUnavailable)

	garbage := edge(t, ca.leaf(t, bundle.LocalPlatformHost), http.StatusOK, []byte("<html>studio</html>"))
	_, err = fetch(garbage, fingerprint)
	require.ErrorContains(t, err, "did not answer with a certificate")

	failing := edge(t, ca.leaf(t, bundle.LocalPlatformHost), http.StatusBadGateway, nil)
	_, err = fetch(failing, fingerprint)
	require.ErrorContains(t, err, "502")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	closed := listener.Addr().String()
	listener.Close()
	_, err = fetch(closed, fingerprint)
	require.ErrorContains(t, err, "reach the remote edge at "+closed)
}
