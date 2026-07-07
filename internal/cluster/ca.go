// Package cluster implements skali's node control plane: the cluster
// certificate authority, one-time join tokens, node enrollment, the worker
// agent, and the master's heartbeat poller. Business logic only — HTTP
// handlers live in internal/api, gRPC transport wiring stays thin.
package cluster

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/crypt"
	"github.com/Hinkolas/skali/internal/store"
)

const (
	// caKeyInfo namespaces the AUTH_SECRET-derived key that seals the CA
	// private key at rest. Pinned: changing it orphans every cluster.
	caKeyInfo = "skali/cluster/ca-key/v1"

	// masterCN identifies the master in the certs it mints for itself.
	// Workers accept control-plane clients only with this CN; since only the
	// master holds the CA key, nobody else can obtain one.
	masterCN = "skali-master"

	caLifetime   = 10 * 365 * 24 * time.Hour
	certLifetime = 365 * 24 * time.Hour

	// notBeforeSkew backdates certs so modest clock drift between nodes
	// doesn't reject a freshly issued cert.
	notBeforeSkew = 5 * time.Minute
)

// CA is the cluster certificate authority, loaded on the master at boot.
type CA struct {
	Cert    *x509.Certificate
	CertPEM []byte
	key     *ecdsa.PrivateKey
}

// EnsureCA loads the cluster CA, generating and persisting it on first boot.
// The private key is sealed with AUTH_SECRET — rotating that secret orphans
// the CA and every node identity signed by it.
func EnsureCA(ctx context.Context, st *store.Store, authSecret string) (*CA, error) {
	sealKey, err := crypt.Key(authSecret, caKeyInfo)
	if err != nil {
		return nil, fmt.Errorf("cluster: derive CA seal key: %w", err)
	}

	row, err := st.GetClusterCA(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		row, err = createCA(ctx, st, sealKey)
	}
	if err != nil {
		return nil, err
	}

	cert, err := parseCertPEM([]byte(row.CertPem))
	if err != nil {
		return nil, fmt.Errorf("cluster: parse stored CA cert: %w", err)
	}
	keyDER, err := crypt.Decrypt(sealKey, row.KeyCipher)
	if err != nil {
		return nil, fmt.Errorf("cluster: unseal CA key (was AUTH_SECRET rotated?): %w", err)
	}
	key, err := x509.ParseECPrivateKey(keyDER)
	if err != nil {
		return nil, fmt.Errorf("cluster: parse CA key: %w", err)
	}
	return &CA{Cert: cert, CertPEM: []byte(row.CertPem), key: key}, nil
}

// generateCA creates a fresh, unpersisted cluster CA.
func generateCA() (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("cluster: generate CA key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "skali-cluster-ca"},
		NotBefore:             time.Now().Add(-notBeforeSkew),
		NotAfter:              time.Now().Add(caLifetime),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("cluster: self-sign CA: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return &CA{Cert: cert, CertPEM: certPEM, key: key}, nil
}

func createCA(ctx context.Context, st *store.Store, sealKey []byte) (store.ClusterCa, error) {
	ca, err := generateCA()
	if err != nil {
		return store.ClusterCa{}, err
	}
	keyDER, err := x509.MarshalECPrivateKey(ca.key)
	if err != nil {
		return store.ClusterCa{}, fmt.Errorf("cluster: marshal CA key: %w", err)
	}
	keyCipher, err := crypt.Encrypt(sealKey, keyDER)
	if err != nil {
		return store.ClusterCa{}, fmt.Errorf("cluster: seal CA key: %w", err)
	}

	row, err := st.CreateClusterCA(ctx, store.CreateClusterCAParams{
		CertPem:   string(ca.CertPEM),
		KeyCipher: keyCipher,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// Boot race: a concurrent first boot won the insert (ON CONFLICT DO
		// NOTHING returned no row). Use the winner's CA.
		return st.GetClusterCA(ctx)
	}
	return row, err
}

// SignNodeCert issues a node identity cert from a CSR. Only the CSR's public
// key is honored — subject and requested extensions are the master's call.
// The node's UUID becomes both CN and DNS SAN, so the master can verify a
// dialed worker with standard hostname verification (ServerName = UUID).
func (ca *CA) SignNodeCert(csrPEM []byte, nodeID uuid.UUID) (certPEM []byte, serialHex string, err error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, "", fmt.Errorf("cluster: CSR is not a PEM certificate request")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, "", fmt.Errorf("cluster: parse CSR: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, "", fmt.Errorf("cluster: CSR signature: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: nodeID.String()},
		DNSNames:     []string{nodeID.String()},
		NotBefore:    time.Now().Add(-notBeforeSkew),
		NotAfter:     time.Now().Add(certLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, csr.PublicKey, ca.key)
	if err != nil {
		return nil, "", fmt.Errorf("cluster: sign node cert: %w", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return certPEM, serial.Text(16), nil
}

// IssueServerCert mints the master's enrollment-listener cert, in memory at
// every boot (the master holds the CA key, so persisting adds nothing). The
// returned chain is [leaf, CA] — enrolling nodes have no trust store yet and
// must see the CA to check it against the fingerprint pinned in their token.
func (ca *CA) IssueServerCert(hosts []string) (tls.Certificate, error) {
	tmpl := &x509.Certificate{
		Subject:     pkix.Name{CommonName: masterCN},
		NotBefore:   time.Now().Add(-notBeforeSkew),
		NotAfter:    time.Now().Add(certLifetime),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	applyHostSANs(tmpl, hosts)
	return ca.issue(tmpl, true)
}

// ServerPEM issues a CA-signed TLS server identity as PEM — trust material
// for components outside this process (e.g. the registry container, which
// mounts its cert as files). Implements the mirror package's Issuer.
func (ca *CA) ServerPEM(cn string, hosts []string, lifetime time.Duration) (certPEM, keyPEM []byte, err error) {
	tmpl := &x509.Certificate{
		Subject:     pkix.Name{CommonName: cn},
		NotBefore:   time.Now().Add(-notBeforeSkew),
		NotAfter:    time.Now().Add(lifetime),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	applyHostSANs(tmpl, hosts)
	return ca.issuePEM(tmpl)
}

// ClientPEM issues the master's TLS client identity as PEM, for clients that
// read certs from disk (dockerd's certs.d) or need raw material (the mirror
// importer's transport). Implements the mirror package's Issuer.
func (ca *CA) ClientPEM() (certPEM, keyPEM []byte, err error) {
	tmpl := &x509.Certificate{
		Subject:     pkix.Name{CommonName: masterCN},
		NotBefore:   time.Now().Add(-notBeforeSkew),
		NotAfter:    time.Now().Add(certLifetime),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	return ca.issuePEM(tmpl)
}

// CAPEM returns the CA certificate PEM. Implements the mirror package's
// Issuer.
func (ca *CA) CAPEM() []byte { return ca.CertPEM }

func applyHostSANs(tmpl *x509.Certificate, hosts []string) {
	for _, h := range hosts {
		if h == "" {
			continue
		}
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
}

// IssueClientCert mints the master's dialing identity for worker NodeService
// connections, in memory at every boot. Workers authorize it by its CN.
func (ca *CA) IssueClientCert() (tls.Certificate, error) {
	tmpl := &x509.Certificate{
		Subject:     pkix.Name{CommonName: masterCN},
		NotBefore:   time.Now().Add(-notBeforeSkew),
		NotAfter:    time.Now().Add(certLifetime),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	return ca.issue(tmpl, false)
}

// issuePEM signs tmpl and returns the identity as PEM pairs, for consumers
// outside this process's memory.
func (ca *CA) issuePEM(tmpl *x509.Certificate) (certPEM, keyPEM []byte, err error) {
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	tmpl.SerialNumber = serial
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("cluster: generate key: %w", err)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, nil, fmt.Errorf("cluster: sign cert: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("cluster: marshal key: %w", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

func (ca *CA) issue(tmpl *x509.Certificate, includeCA bool) (tls.Certificate, error) {
	serial, err := randomSerial()
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl.SerialNumber = serial
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("cluster: generate key: %w", err)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("cluster: sign cert: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, err
	}
	chain := [][]byte{der}
	if includeCA {
		chain = append(chain, ca.Cert.Raw)
	}
	return tls.Certificate{Certificate: chain, PrivateKey: key, Leaf: leaf}, nil
}

// Pool returns a cert pool containing exactly the cluster CA.
func (ca *CA) Pool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	return pool
}

// Fingerprint is the CA-pinning value embedded in join tokens: the first 16
// bytes of sha256(cert), hex-encoded (32 chars). 128 bits is ample for a
// second-preimage pin.
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:16])
}

func randomSerial() (*big.Int, error) {
	// 128-bit random serials, the standard collision-safe choice.
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("cluster: generate serial: %w", err)
	}
	return serial, nil
}

func parseCertPEM(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("cluster: not a PEM certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}
