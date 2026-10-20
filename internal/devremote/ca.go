package devremote

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/Hinkolas/skali/internal/bundle"
)

// caURLPath is where a local platform serves its development CA on the
// platform host (api.DevCAPath below the /api prefix).
const caURLPath = "/api/dev/ca.crt"

// ErrCAUnavailable reports an edge that answers but serves no development
// CA: a platform from before this feature, or not a skali dev edge at all.
var ErrCAUnavailable = errors.New("the remote edge serves no development CA; " +
	"update skali on the remote machine and run skali dev there once, " +
	"or check that the host leads to a skali dev platform")

// ErrFingerprintMismatch reports a CA that is not the one the developer
// pinned. The message deliberately leaves out the fingerprint that was
// seen: printing it would invite pasting it back, which defeats the check.
var ErrFingerprintMismatch = errors.New("the remote edge serves a development CA that does not match the fingerprint; " +
	"copy it again from skali dev status on the remote machine (a skali dev reset there creates a new CA). " +
	"If it still differs, something between the two machines answers in the remote edge's place")

// RemoteCA is a remote installation's development CA that matched its
// pinned fingerprint.
type RemoteCA struct {
	PEM         []byte
	Certificate *x509.Certificate
	Fingerprint string
}

// CommonName is the subject trust stores list the CA under.
func (ca *RemoteCA) CommonName() string { return ca.Certificate.Subject.CommonName }

type dialFunc func(ctx context.Context, network, address string) (net.Conn, error)

var defaultDialer = &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}

// FetchCA retrieves the development CA of the installation whose edge
// answers on host's HTTPS port and accepts it only when it matches
// fingerprint, in the form ParseFingerprint returns. It then verifies the
// certificate the edge presented on the same connection against that CA
// for the platform host, which proves the host serves that installation
// and not merely a copy of its public certificate.
func FetchCA(ctx context.Context, host, fingerprint string) (*RemoteCA, error) {
	return fetchCA(ctx, hostPort(host, RemoteHTTPSPort()), fingerprint, defaultDialer.DialContext)
}

func fetchCA(ctx context.Context, address, fingerprint string, dial dialFunc) (*RemoteCA, error) {
	platformHost := bundle.LocalPlatformHost
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dial(ctx, network, address)
		},
		// The chain cannot be verified during the handshake: the CA it
		// leads to is what this request fetches. Nothing secret is sent,
		// the answer only counts when it matches the pinned fingerprint,
		// and the chain is verified against it right after.
		TLSClientConfig:       &tls.Config{ServerName: platformHost, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12},
		DisableKeepAlives:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport:     transport,
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+platformHost+caURLPath, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("reach the remote edge at %s: %w", address, err)
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, ErrCAUnavailable
	default:
		return nil, fmt.Errorf("the remote edge at %s answered %s for its development CA", address, response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil {
		return nil, fmt.Errorf("read the development CA from %s: %w", address, err)
	}
	block, _ := pem.Decode(body)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("the remote edge at %s did not answer with a certificate; "+
			"check that the host leads to a skali dev platform", address)
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse the development CA from %s: %w", address, err)
	}
	if Fingerprint(certificate.Raw) != fingerprint {
		return nil, ErrFingerprintMismatch
	}
	if !certificate.IsCA || !certificate.BasicConstraintsValid {
		return nil, errors.New("the pinned certificate is not a CA certificate")
	}
	if response.TLS == nil || len(response.TLS.PeerCertificates) == 0 {
		return nil, fmt.Errorf("the remote edge at %s presented no certificate", address)
	}
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	intermediates := x509.NewCertPool()
	for _, peer := range response.TLS.PeerCertificates[1:] {
		intermediates.AddCert(peer)
	}
	if _, err := response.TLS.PeerCertificates[0].Verify(x509.VerifyOptions{
		DNSName:       platformHost,
		Roots:         roots,
		Intermediates: intermediates,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		return nil, fmt.Errorf("the remote edge at %s presents a certificate the pinned CA did not issue: %w", address, err)
	}
	return &RemoteCA{
		PEM:         pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}),
		Certificate: certificate,
		Fingerprint: fingerprint,
	}, nil
}

// Pin writes the CA to PinnedCAPath for the trust stores to import and
// returns that path.
func (ca *RemoteCA) Pin() (string, error) {
	path, err := PinnedCAPath()
	if err != nil {
		return "", err
	}
	if err := writeFileAtomic(path, ca.PEM, 0o644); err != nil {
		return "", fmt.Errorf("devremote: write %s: %w", path, err)
	}
	return path, nil
}
