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
	mathrand "math/rand/v2"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

type EnrollmentClient struct {
	Endpoint    string
	Endpoints   []string
	Token       string
	Timeout     time.Duration
	RetryBudget time.Duration
	OnRetry     func(attempt int, delay time.Duration, err error)
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
	endpoints := []string{endpoint}
	for _, candidate := range c.Endpoints {
		normalized, err := NormalizeEndpoint(candidate)
		if err != nil {
			return err
		}
		if !slices.Contains(endpoints, normalized) {
			endpoints = append(endpoints, normalized)
		}
	}
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}

	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	budget := c.RetryBudget
	if budget <= 0 {
		budget = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	transport := &http.Transport{TLSClientConfig: pinnedTLSConfig(token.CAPin)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: timeout, Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	for attempt := 0; ; attempt++ {
		err := enrollmentAttempt(ctx, client, endpoints[attempt%len(endpoints)], c.Token, path, body, output)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return fmt.Errorf("enrollment request interrupted: %w", ctx.Err())
		}
		if !retryableEnrollmentError(err) {
			return err
		}
		delay := time.Second << min(attempt, 3)
		delay = min(10*time.Second, delay+time.Duration(mathrand.Int64N(int64(delay/2)+1)))
		var problem *EnrollmentError
		if errors.As(err, &problem) && problem.RetryAfter > delay {
			delay = problem.RetryAfter
		}
		if c.OnRetry != nil {
			c.OnRetry(attempt+1, delay, err)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("enrollment response not confirmed: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

// EnrollmentError preserves the protocol classification without parsing human copy.
type EnrollmentError struct {
	Status     int
	Code       string
	Message    string
	RetryAfter time.Duration
}

func (e *EnrollmentError) Error() string { return "coordinator rejected enrollment: " + e.Message }

type coordinatorTrustError struct{ error }

func enrollmentAttempt(ctx context.Context, client *http.Client, endpoint, token, path string, body []byte, output any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(endpoint, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+NormalizeToken(token))
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
		var problem struct {
			Code    string `json:"code"`
			Message string `json:"error"`
		}
		_ = json.Unmarshal(data, &problem)
		if problem.Message == "" {
			problem.Message = fmt.Sprintf("HTTP %d", response.StatusCode)
		}
		return &EnrollmentError{Status: response.StatusCode, Code: problem.Code, Message: strings.ReplaceAll(problem.Message, NormalizeToken(token), "[redacted]"), RetryAfter: retryAfter(response.Header.Get("Retry-After"), time.Now())}
	}
	if err := json.Unmarshal(data, output); err != nil {
		return fmt.Errorf("decode coordinator response: %w", err)
	}
	return nil
}

func retryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(min(seconds, 120)) * time.Second
	}
	if deadline, err := http.ParseTime(value); err == nil {
		return max(0, deadline.Sub(now))
	}
	return 0
}

func retryableEnrollmentError(err error) bool {
	var trust *coordinatorTrustError
	if errors.As(err, &trust) || errors.Is(err, context.Canceled) {
		return false
	}
	var problem *EnrollmentError
	if errors.As(err, &problem) {
		switch problem.Status {
		case 408, 429, 500, 502, 503, 504:
			return true
		}
		return false
	}
	var dns *net.DNSError
	if errors.As(err, &dns) && dns.IsNotFound {
		return false
	}
	var netError net.Error
	return errors.As(err, &netError) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func pinnedTLSConfig(pin string) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // replaced by exact CA pin verification below
		VerifyConnection: func(connection tls.ConnectionState) error {
			if len(connection.PeerCertificates) == 0 {
				return &coordinatorTrustError{errors.New("coordinator presented no certificate")}
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
				return &coordinatorTrustError{errors.New("coordinator CA does not match the enrollment token")}
			}
			_, err := connection.PeerCertificates[0].Verify(x509.VerifyOptions{
				Roots: roots, Intermediates: intermediates,
				KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			})
			if err != nil {
				return &coordinatorTrustError{fmt.Errorf("verify coordinator certificate: %w", err)}
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
				return &coordinatorTrustError{errors.New("coordinator presented no certificate")}
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
