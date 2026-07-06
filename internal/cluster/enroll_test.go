package cluster

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	"github.com/Hinkolas/skali/internal/clusterpb"
	"github.com/Hinkolas/skali/internal/hostinfo"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/testdb"
)

const testAuthSecret = "test-auth-secret-that-is-32-chars-long!!"

// startMaster boots a real enrollment gRPC server (TLS, port 0) over a fresh
// test database, mirroring the runServe wiring.
func startMaster(t *testing.T) (*store.Store, *CA, string) {
	t.Helper()
	ctx := context.Background()

	st := store.NewStore(testdb.New(t))
	ca, err := EnsureCA(ctx, st, testAuthSecret)
	require.NoError(t, err)

	srv, err := NewMasterServer(st, ca, nil)
	require.NoError(t, err)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go srv.Serve(lis) //nolint:errcheck
	t.Cleanup(srv.Stop)

	return st, ca, lis.Addr().String()
}

// warmSampler returns a sampler with two synchronous samples taken, so
// heartbeats carry a metrics snapshot.
func warmSampler(t *testing.T) *hostinfo.Sampler {
	t.Helper()
	s := hostinfo.New(t.TempDir())
	s.SampleNow(context.Background())
	s.SampleNow(context.Background())
	return s
}

func enrollOptions(t *testing.T, masterAddr, token string) EnrollOptions {
	t.Helper()
	return EnrollOptions{
		MasterAddr:    masterAddr,
		Token:         token,
		AdvertiseAddr: "127.0.0.1:7444",
		GRPCAddr:      ":7444",
		DataDir:       t.TempDir(),
	}
}

func TestEnrollHappyPath(t *testing.T) {
	ctx := context.Background()
	st, ca, addr := startMaster(t)

	token, row, err := MintJoinToken(ctx, st, ca, []string{"worker", "edge"}, nil)
	require.NoError(t, err)
	require.Nil(t, row.UsedAt)

	opts := enrollOptions(t, addr, token)
	identity, roles, err := RunEnroll(ctx, opts)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"worker", "edge"}, roles)

	// Node row created with the token's roles and the issued cert's serial.
	node, err := st.GetNodeByID(ctx, identity.NodeID)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"worker", "edge"}, node.Roles)
	require.Equal(t, "127.0.0.1:7444", node.AdvertiseAddr)
	require.Equal(t, "offline", node.Status)
	require.NotNil(t, node.CertSerial)

	// Identity on disk: cert chains to the CA, SAN is the node UUID, the key
	// is private (0600).
	require.Equal(t, *node.CertSerial, identity.Cert.Leaf.SerialNumber.Text(16))
	require.Equal(t, []string{identity.NodeID.String()}, identity.Cert.Leaf.DNSNames)
	_, err = identity.Cert.Leaf.Verify(x509.VerifyOptions{
		Roots:     identity.CAPool,
		DNSName:   identity.NodeID.String(),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	require.NoError(t, err)
	for name, mode := range map[string]os.FileMode{
		nodeKeyFile: 0o600, nodeMetaFile: 0o600, nodeCertFile: 0o644, caCertFile: 0o644,
	} {
		info, err := os.Stat(filepath.Join(opts.DataDir, name))
		require.NoError(t, err)
		require.Equal(t, mode, info.Mode().Perm(), name)
	}

	// Token burned.
	burned, err := st.GetJoinTokenByID(ctx, row.ID)
	require.NoError(t, err)
	require.NotNil(t, burned.UsedAt)

	// Burned token admits no second node.
	_, _, err = RunEnroll(ctx, enrollOptions(t, addr, token))
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestEnrollRejectsWrongSecret(t *testing.T) {
	ctx := context.Background()
	st, ca, addr := startMaster(t)

	token, row, err := MintJoinToken(ctx, st, ca, []string{"worker"}, nil)
	require.NoError(t, err)

	// Same token id and CA pin, different secret.
	id, _, fp, err := ParseJoinToken(token)
	require.NoError(t, err)
	require.Equal(t, row.ID, id)
	forged := id.String() + ".AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA." + fp

	_, _, err = RunEnroll(ctx, enrollOptions(t, addr, forged))
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	// The real token still works — a forgery attempt must not burn it.
	_, _, err = RunEnroll(ctx, enrollOptions(t, addr, token))
	require.NoError(t, err)
}

func TestEnrollRejectsExpiredToken(t *testing.T) {
	ctx := context.Background()
	st, ca, addr := startMaster(t)

	// Insert an already-expired token directly (MintJoinToken pins the TTL).
	id, err := uuid.NewV7()
	require.NoError(t, err)
	secret := "expired-secret-expired-secret-expired-secr"
	hash := sha256.Sum256([]byte(secret))
	_, err = st.CreateJoinToken(ctx, store.CreateJoinTokenParams{
		ID: id, Hash: hash[:], Roles: []string{"worker"},
		ExpiresAt: time.Now().Add(-time.Minute),
	})
	require.NoError(t, err)

	token := id.String() + "." + secret + "." + Fingerprint(ca.Cert)
	_, _, err = RunEnroll(ctx, enrollOptions(t, addr, token))
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestEnrollRejectsWrongCAPin(t *testing.T) {
	ctx := context.Background()
	st, ca, addr := startMaster(t)

	token, _, err := MintJoinToken(ctx, st, ca, []string{"worker"}, nil)
	require.NoError(t, err)

	// Rewrite the pin as if the token had been minted by a different cluster:
	// the client must refuse the master during the handshake, before any
	// token secret crosses the wire.
	other, err := generateCA()
	require.NoError(t, err)
	id, secret, _, err := ParseJoinToken(token)
	require.NoError(t, err)
	forged := id.String() + "." + secret + "." + Fingerprint(other.Cert)

	shortCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, _, err = RunEnroll(shortCtx, enrollOptions(t, addr, forged))
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not match")

	// And the untouched token still enrolls fine.
	_, _, err = RunEnroll(ctx, enrollOptions(t, addr, token))
	require.NoError(t, err)
}

func TestAgentMTLS(t *testing.T) {
	ctx := context.Background()
	st, ca, addr := startMaster(t)

	token, _, err := MintJoinToken(ctx, st, ca, []string{"worker"}, nil)
	require.NoError(t, err)
	identity, _, err := RunEnroll(ctx, enrollOptions(t, addr, token))
	require.NoError(t, err)

	// Run the worker's NodeService on a random port.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	agent := NewAgentServer(identity, warmSampler(t))
	go agent.Serve(lis) //nolint:errcheck
	t.Cleanup(agent.Stop)

	dial := func(cert tls.Certificate, withCert bool) error {
		cfg := &tls.Config{
			MinVersion: tls.VersionTLS13,
			RootCAs:    ca.Pool(),
			ServerName: identity.NodeID.String(),
		}
		if withCert {
			cfg.Certificates = []tls.Certificate{cert}
		}
		conn, err := grpc.NewClient(lis.Addr().String(),
			grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
		if err != nil {
			return err
		}
		defer conn.Close()
		callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		resp, err := clusterpb.NewNodeServiceClient(conn).Heartbeat(callCtx, &clusterpb.HeartbeatRequest{})
		if err != nil {
			return err
		}
		require.Equal(t, identity.NodeID.String(), resp.GetNodeId())
		return nil
	}

	// The master's client cert is accepted.
	masterCert, err := ca.IssueClientCert()
	require.NoError(t, err)
	require.NoError(t, dial(masterCert, true))

	// No client cert: refused.
	require.Error(t, dial(tls.Certificate{}, false))

	// A CA-signed cert that is not the master's (another node's identity,
	// which carries ClientAuth EKU) is refused by the CN check.
	require.Error(t, dial(identity.Cert, true))
}
