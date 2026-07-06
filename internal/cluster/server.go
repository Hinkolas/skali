package cluster

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/Hinkolas/skali/internal/clusterpb"
	"github.com/Hinkolas/skali/internal/hostinfo"
)

// NodeServer is the worker-side NodeService: the surface the master drives.
// Heartbeat only for now; the container-lifecycle RPCs (the Executor seam)
// join it later.
type NodeServer struct {
	clusterpb.UnimplementedNodeServiceServer
	nodeID  string
	sampler *hostinfo.Sampler
}

func (s *NodeServer) Heartbeat(ctx context.Context, _ *clusterpb.HeartbeatRequest) (*clusterpb.HeartbeatResponse, error) {
	return heartbeatResponse(s.nodeID, s.sampler), nil
}

// NewAgentServer builds the worker's gRPC server. Every connection is mTLS:
// the client must present a cluster-CA-signed cert with the master's CN —
// only the master holds the CA key, so nobody else can mint one.
func NewAgentServer(id *Identity, sampler *hostinfo.Sampler) *grpc.Server {
	tlsCfg := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{id.Cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    id.CAPool,
		VerifyPeerCertificate: func(_ [][]byte, chains [][]*x509.Certificate) error {
			// chains is always populated under RequireAndVerifyClientCert.
			if len(chains) == 0 || len(chains[0]) == 0 {
				return errors.New("cluster: no verified client chain")
			}
			if cn := chains[0][0].Subject.CommonName; cn != masterCN {
				return fmt.Errorf("cluster: client %q is not the master", cn)
			}
			return nil
		},
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsCfg)))
	clusterpb.RegisterNodeServiceServer(srv, &NodeServer{nodeID: id.NodeID.String(), sampler: sampler})
	return srv
}

// ServeAgent runs the worker's gRPC server until ctx is canceled.
func ServeAgent(ctx context.Context, id *Identity, grpcAddr string, sampler *hostinfo.Sampler) error {
	lis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		return err
	}
	srv := NewAgentServer(id, sampler)

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(lis) }()

	slog.InfoContext(ctx, "agent serving", "node_id", id.NodeID, "grpc_addr", grpcAddr)
	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		srv.GracefulStop()
		return nil
	}
}
