package clusterstate

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
	"time"
)

type EnrollmentClient struct {
	Endpoint string
	Token    string
	Timeout  time.Duration
}

func (c EnrollmentClient) Preflight(ctx context.Context, host HostFacts) (PreflightResponse, error) {
	var response PreflightResponse
	err := c.enrollmentRequest(ctx, "/v1/enroll/preflight", PreflightRequest{Host: host}, &response)
	return response, err
}

func (c EnrollmentClient) Enroll(ctx context.Context, host HostFacts, csr []byte) (EnrollResponse, error) {
	var response EnrollResponse
	err := c.enrollmentRequest(ctx, "/v1/enroll", EnrollRequest{Host: host, CSR: string(csr)}, &response)
	return response, err
}

func (c EnrollmentClient) enrollmentRequest(ctx context.Context, path string, input, output any) error {
	token, err := ParseToken(c.Token)
	if err != nil {
		return err
	}
	endpoint, err := NormalizeEndpoint(c.Endpoint)
	if err != nil {
		return err
	}
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	client := &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{TLSClientConfig: pinnedTLSConfig(token.CAPin)},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(endpoint, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.Token)
	response, err := client.Do(request)
	if err != nil {
		return classifyEnrollmentError(endpoint, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxRequestBody))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var problem map[string]string
		if json.Unmarshal(data, &problem) == nil && problem["error"] != "" {
			return fmt.Errorf("coordinator rejected enrollment: %s", problem["error"])
		}
		return fmt.Errorf("coordinator returned HTTP %d", response.StatusCode)
	}
	if err := json.Unmarshal(data, output); err != nil {
		return fmt.Errorf("decode coordinator response: %w", err)
	}
	return nil
}

func pinnedTLSConfig(pin string) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // replaced by exact CA pin verification below
		VerifyConnection: func(connection tls.ConnectionState) error {
			if len(connection.PeerCertificates) == 0 {
				return errors.New("coordinator presented no certificate")
			}
			roots := x509.NewCertPool()
			intermediates := x509.NewCertPool()
			var pinned bool
			for index, certificate := range connection.PeerCertificates {
				sum := sha256.Sum256(certificate.Raw)
				if "sha256:"+hex.EncodeToString(sum[:]) == pin && certificate.IsCA {
					roots.AddCert(certificate)
					pinned = true
					continue
				}
				if index > 0 {
					intermediates.AddCert(certificate)
				}
			}
			if !pinned {
				return errors.New("coordinator CA does not match the enrollment token")
			}
			_, err := connection.PeerCertificates[0].Verify(x509.VerifyOptions{
				Roots: roots, Intermediates: intermediates,
				KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			})
			if err != nil {
				return fmt.Errorf("verify coordinator certificate: %w", err)
			}
			return nil
		},
	}
}

func classifyEnrollmentError(endpoint string, err error) error {
	var netError interface {
		Timeout() bool
	}
	var dnsError *net.DNSError
	switch {
	case errors.As(err, &netError) && netError.Timeout():
		return fmt.Errorf("coordinator %s timed out: %w", endpoint, err)
	case errors.As(err, &dnsError):
		return fmt.Errorf("coordinator %s DNS lookup failed for %s: %w",
			endpoint, dnsError.Name, err)
	case strings.Contains(strings.ToLower(err.Error()), "connection refused"):
		return fmt.Errorf("coordinator %s refused the connection: %w", endpoint, err)
	default:
		return fmt.Errorf("connect to coordinator %s: %w", endpoint, err)
	}
}

func NewAgentKeyAndCSR(nodeID, nodeName string) (privateKeyPEM, csrPEM []byte, err error) {
	if nodeID == "" || nodeName == "" {
		return nil, nil, errors.New("node ID and name are required for agent identity")
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:            pkix.Name{CommonName: nodeID},
		DNSNames:           []string{nodeName},
		SignatureAlgorithm: x509.PureEd25519,
	}, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}), nil
}

// AgentTLSConfig builds mTLS for the long-lived polling client.
func AgentTLSConfig(caPEM, certPEM, keyPEM []byte) (*tls.Config, error) {
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("agent CA contains no certificate")
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate},
		RootCAs: pool, InsecureSkipVerify: true,
		VerifyConnection: func(connection tls.ConnectionState) error {
			if len(connection.PeerCertificates) == 0 {
				return errors.New("coordinator presented no certificate")
			}
			intermediates := x509.NewCertPool()
			for _, peer := range connection.PeerCertificates[1:] {
				intermediates.AddCert(peer)
			}
			_, err := connection.PeerCertificates[0].Verify(x509.VerifyOptions{
				Roots: pool, Intermediates: intermediates,
				KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			})
			return err
		},
	}, nil
}

// randomSerial is retained here for certificate-rotation tests and avoids
// callers duplicating crypto/rand handling.
func randomSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}
