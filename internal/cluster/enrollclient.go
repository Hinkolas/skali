package cluster

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net"
	"path/filepath"
	"runtime"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/Hinkolas/skali/internal/clusterpb"
	"github.com/Hinkolas/skali/internal/mirror"
	"github.com/Hinkolas/skali/internal/version"
)

// EnrollOptions parameterize a node's enrollment against a master.
type EnrollOptions struct {
	MasterAddr    string
	Token         string
	AdvertiseAddr string // empty = auto-detect (outbound interface toward the master + GRPCAddr's port)
	GRPCAddr      string // this node's future NodeService listen address
	DataDir       string
	CertsDir      string // dockerd trust root; empty = /etc/docker/certs.d (the seam exists for tests)
}

// RunEnroll performs the worker side of enrollment: authenticate the master
// via the CA fingerprint pinned in the join token, submit a CSR, and persist
// the issued identity under DataDir. Returns the identity and granted roles.
func RunEnroll(ctx context.Context, opts EnrollOptions) (*Identity, []string, error) {
	_, _, pinnedFP, err := ParseJoinToken(opts.Token)
	if err != nil {
		return nil, nil, err
	}

	advertiseAddr := opts.AdvertiseAddr
	if advertiseAddr == "" {
		advertiseAddr, err = detectAdvertiseAddr(opts.MasterAddr, opts.GRPCAddr)
		if err != nil {
			return nil, nil, fmt.Errorf("cluster: cannot auto-detect --advertise-addr (%w); pass it explicitly", err)
		}
	} else if _, _, err := net.SplitHostPort(advertiseAddr); err != nil {
		return nil, nil, fmt.Errorf("cluster: --advertise-addr must be host:port: %w", err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		return nil, nil, err
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})

	conn, err := grpc.NewClient(opts.MasterAddr,
		grpc.WithTransportCredentials(credentials.NewTLS(pinnedTLSConfig(pinnedFP))))
	if err != nil {
		return nil, nil, err
	}
	defer conn.Close()

	resp, err := clusterpb.NewEnrollmentServiceClient(conn).Enroll(ctx, &clusterpb.EnrollRequest{
		Token:         opts.Token,
		CsrPem:        csrPEM,
		AdvertiseAddr: advertiseAddr,
		Hostname:      hostnameOrDefault(),
		Arch:          runtime.GOARCH,
		Os:            runtime.GOOS,
		SkalidVersion: version.Version,
	})
	if err != nil {
		return nil, nil, err
	}

	nodeID, err := uuid.Parse(resp.GetNodeId())
	if err != nil {
		return nil, nil, fmt.Errorf("cluster: master returned invalid node id %q", resp.GetNodeId())
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	meta := Identity{
		NodeID: nodeID, MasterAddr: opts.MasterAddr,
		AdvertiseAddr: advertiseAddr, RegistryAddr: resp.GetRegistryAddr(),
	}
	if err := SaveIdentity(opts.DataDir, meta, keyPEM, resp.GetCertPem(), resp.GetCaPem()); err != nil {
		return nil, nil, err
	}
	installRegistryTrust(opts.CertsDir, resp.GetRegistryAddr(), resp.GetCaPem(), resp.GetCertPem(), keyPEM)
	id, err := LoadIdentity(opts.DataDir)
	if err != nil {
		return nil, nil, err
	}
	return id, resp.GetRoles(), nil
}

// installRegistryTrust points this node's dockerd at the cluster image
// mirror: the CA to verify it, the node identity to authenticate against it.
// Best-effort — enrollment must not fail on an unwritable /etc/docker (the
// node still joins; only mirror pulls need the trust), so failures print
// what to install manually.
func installRegistryTrust(certsDir, registryAddr string, caPEM, certPEM, keyPEM []byte) {
	if registryAddr == "" {
		return
	}
	if certsDir == "" {
		certsDir = mirror.DefaultCertsDir
	}
	if err := mirror.InstallDockerCerts(certsDir, registryAddr, caPEM, certPEM, keyPEM); err != nil {
		slog.Warn("cannot install docker trust for the cluster registry; "+
			"copy ca.crt, node.crt (as client.cert) and node.key (as client.key) from the identity dir manually",
			"dir", filepath.Join(certsDir, registryAddr), "err", err)
	}
}

// pinnedTLSConfig authenticates a master the node does not yet trust: standard
// verification is disabled and replaced by a check that the presented chain
// contains the CA pinned in the join token, and that the leaf chains to it.
// A MITM cannot satisfy the pin without the CA key, and the subsequent Enroll
// call carries the token secret only over this authenticated channel.
func pinnedTLSConfig(pinnedFP string) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // trust is the fingerprint pin below, not the system roots
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return fmt.Errorf("cluster: master presented no certificates")
			}
			certs := make([]*x509.Certificate, 0, len(rawCerts))
			for _, raw := range rawCerts {
				cert, err := x509.ParseCertificate(raw)
				if err != nil {
					return err
				}
				certs = append(certs, cert)
			}

			// Locate the pinned CA among the presented certs (the master
			// serves [leaf, CA] precisely for this).
			pool := x509.NewCertPool()
			found := false
			for _, cert := range certs[1:] {
				if cert.IsCA && bytes.Equal(cert.RawIssuer, cert.RawSubject) &&
					Fingerprint(cert) == pinnedFP {
					pool.AddCert(cert)
					found = true
				}
			}
			if !found {
				return ErrCAMismatch
			}
			_, err := certs[0].Verify(x509.VerifyOptions{
				Roots:     pool,
				KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			})
			if err != nil {
				return fmt.Errorf("%w: %w", ErrCAMismatch, err)
			}
			return nil
		},
	}
}

// detectAdvertiseAddr guesses the address the master should dial back: the
// local interface used to reach the master, on the agent's gRPC port. No
// packets are sent — UDP "dialing" only resolves the route.
func detectAdvertiseAddr(masterAddr, grpcAddr string) (string, error) {
	_, port, err := net.SplitHostPort(grpcAddr)
	if err != nil {
		return "", fmt.Errorf("invalid gRPC addr %q: %w", grpcAddr, err)
	}
	conn, err := net.Dial("udp", masterAddr)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	local, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return "", fmt.Errorf("unexpected local addr type %T", conn.LocalAddr())
	}
	return net.JoinHostPort(local.IP.String(), port), nil
}
