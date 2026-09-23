package localdev

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// The development CA signs every route certificate of the local platform:
// cert-manager holds the key as the skali ClusterIssuer's secret, the
// developer's trust store (and the CLI) hold the certificate. It is
// generated once per installation into the cluster's state directory and
// removed with it by skali dev reset, so a recreated platform gets a fresh
// CA and the old one, if still trusted, signs nothing anymore.
const (
	caCertFile = "ca.crt"
	caKeyFile  = "ca.key"
	// caValidity is deliberately long: rotation is skali dev reset.
	caValidity = 10 * 365 * 24 * time.Hour
)

// CA is the loaded development CA.
type CA struct {
	CertPEM []byte
	KeyPEM  []byte
	// Fingerprint is the lowercase hex SHA-256 of the certificate DER, the
	// identity a trust store shows.
	Fingerprint string
	// CommonName is the subject the trust store lists the CA under.
	CommonName string
	// CertPath is where the certificate lives on disk, for the developer
	// to import by hand when the automatic trust step cannot.
	CertPath string
}

// CAPaths names the certificate and key files of the current cluster.
func CAPaths() (certPath, keyPath string, err error) {
	dir, err := ClusterDir(ClusterName())
	if err != nil {
		return "", "", err
	}
	return filepath.Join(dir, caCertFile), filepath.Join(dir, caKeyFile), nil
}

// GenerateCA creates a fresh ECDSA P-256 CA for the current cluster and
// writes it into the state directory, replacing any previous one. The
// subject carries a random suffix so two generations (a reset in between)
// stay distinguishable in a keychain that lists both.
func GenerateCA() (*CA, error) {
	certPath, keyPath, err := CAPaths()
	if err != nil {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("localdev: generate development CA key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("localdev: generate development CA serial: %w", err)
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return nil, fmt.Errorf("localdev: generate development CA name: %w", err)
	}
	commonName := fmt.Sprintf("skali local dev CA (%s, %s)", ClusterName(), hex.EncodeToString(suffix))
	now := time.Now()
	template := x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   commonName,
			Organization: []string{"skali local development"},
		},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		// Leaf certificates only: cert-manager signs route certificates
		// directly with this CA, and nothing may mint intermediates.
		MaxPathLen:     0,
		MaxPathLenZero: true,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("localdev: create development CA certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("localdev: encode development CA key: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		return nil, fmt.Errorf("localdev: create state directory: %w", err)
	}
	// The key first: a certificate without its key would load as a CA the
	// cluster cannot sign with, while a key without its certificate reads
	// as absent and regenerates.
	if err := writeFileAtomic(keyPath, keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("localdev: write development CA key: %w", err)
	}
	if err := writeFileAtomic(certPath, certPEM, 0o644); err != nil {
		return nil, fmt.Errorf("localdev: write development CA certificate: %w", err)
	}
	return newCA(certPEM, keyPEM, certPath)
}

// LoadCA reads the development CA of the current cluster; ErrNotInstalled
// when there is none.
func LoadCA() (*CA, error) {
	certPath, keyPath, err := CAPaths()
	if err != nil {
		return nil, err
	}
	certPEM, err := os.ReadFile(certPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotInstalled
	}
	if err != nil {
		return nil, fmt.Errorf("localdev: read development CA certificate: %w", err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotInstalled
	}
	if err != nil {
		return nil, fmt.Errorf("localdev: read development CA key: %w", err)
	}
	return newCA(certPEM, keyPEM, certPath)
}

// EnsureCA loads the installation's CA. A fresh installation generates
// one; an existing installation whose CA files are gone is refused, since
// a regenerated CA would silently stop matching the certificates already
// issued in the cluster and the one the developer trusts.
func EnsureCA(freshInstall bool) (*CA, error) {
	ca, err := LoadCA()
	switch {
	case err == nil:
		return ca, nil
	case !errors.Is(err, ErrNotInstalled):
		return nil, err
	case freshInstall:
		return GenerateCA()
	default:
		certPath, _, _ := CAPaths()
		return nil, fmt.Errorf("the local platform's development CA is missing (%s); run skali dev reset to delete the local platform and its data, then skali dev to recreate it", certPath)
	}
}

// LocalCAPEM returns the certificate of the current cluster's development
// CA for clients that talk to the local edge, or false when the local
// platform is not installed. Every failure reads as absent: a client
// without the CA fails its TLS handshake with a clear error later.
func LocalCAPEM() ([]byte, bool) {
	certPath, _, err := CAPaths()
	if err != nil {
		return nil, false
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, false
	}
	return certPEM, true
}

// Pool is a certificate pool holding just this CA.
func (ca *CA) Pool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.CertPEM)
	return pool
}

func newCA(certPEM, keyPEM []byte, certPath string) (*CA, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("localdev: %s does not hold a PEM certificate", certPath)
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("localdev: parse development CA certificate: %w", err)
	}
	if !certificate.IsCA {
		return nil, fmt.Errorf("localdev: %s is not a CA certificate", certPath)
	}
	sum := sha256.Sum256(certificate.Raw)
	return &CA{
		CertPEM:     certPEM,
		KeyPEM:      keyPEM,
		Fingerprint: hex.EncodeToString(sum[:]),
		CommonName:  certificate.Subject.CommonName,
		CertPath:    certPath,
	}, nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return err
	}
	return nil
}
