package cluster

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	"github.com/Hinkolas/skali/internal/clusterpb"
	"github.com/Hinkolas/skali/internal/engine"
	"github.com/Hinkolas/skali/internal/hostinfo"
)

// NodeServer is the worker-side NodeService: the surface the master drives.
// Heartbeat plus the imperative container lifecycle. The node is dumb but
// not gullible: it executes what the master sends after re-checking the
// invariants that protect the host (ownership label, a valid kind).
type NodeServer struct {
	clusterpb.UnimplementedNodeServiceServer
	nodeID     string
	sampler    *hostinfo.Sampler
	eng        engine.Engine
	containers *engine.Sampler
	inventory  *engine.InventorySampler
}

func (s *NodeServer) Heartbeat(ctx context.Context, _ *clusterpb.HeartbeatRequest) (*clusterpb.HeartbeatResponse, error) {
	// Cache-only: all samplers answer from their latest reading; a heartbeat
	// never blocks on the engine or the kernel.
	resp := heartbeatResponse(s.nodeID, s.sampler)
	resp.Containers = containerReport(s.containers)
	resp.Inventory = inventoryReport(s.inventory)
	return resp, nil
}

func (s *NodeServer) CreateContainer(ctx context.Context, req *clusterpb.CreateContainerRequest) (*clusterpb.CreateContainerResponse, error) {
	spec := specFromProto(req.GetSpec())
	if err := engine.ValidateKind(spec.Labels[engine.LabelKind]); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	c, err := engine.Deploy(ctx, s.eng, spec, pullFromProto(req.GetPullPolicy()), req.GetStart())
	if err != nil {
		return nil, grpcEngineErr(err)
	}
	slog.InfoContext(ctx, "container created", "id", c.ID, "name", c.Name, "image", c.Image, "started", req.GetStart())
	return &clusterpb.CreateContainerResponse{Container: containerInfoProto(c, nil)}, nil
}

func (s *NodeServer) StartContainer(ctx context.Context, req *clusterpb.StartContainerRequest) (*clusterpb.StartContainerResponse, error) {
	if err := s.eng.Start(ctx, req.GetContainerId()); err != nil {
		return nil, grpcEngineErr(err)
	}
	c, err := s.eng.Inspect(ctx, req.GetContainerId())
	if err != nil {
		return nil, grpcEngineErr(err)
	}
	return &clusterpb.StartContainerResponse{Container: containerInfoProto(c, nil)}, nil
}

func (s *NodeServer) StopContainer(ctx context.Context, req *clusterpb.StopContainerRequest) (*clusterpb.StopContainerResponse, error) {
	timeout := time.Duration(req.GetTimeoutSeconds()) * time.Second
	if err := s.eng.Stop(ctx, req.GetContainerId(), timeout); err != nil {
		return nil, grpcEngineErr(err)
	}
	c, err := s.eng.Inspect(ctx, req.GetContainerId())
	if err != nil {
		return nil, grpcEngineErr(err)
	}
	return &clusterpb.StopContainerResponse{Container: containerInfoProto(c, nil)}, nil
}

func (s *NodeServer) RemoveContainer(ctx context.Context, req *clusterpb.RemoveContainerRequest) (*clusterpb.RemoveContainerResponse, error) {
	if err := s.eng.Remove(ctx, req.GetContainerId(), req.GetForce()); err != nil {
		return nil, grpcEngineErr(err)
	}
	slog.InfoContext(ctx, "container removed", "id", req.GetContainerId())
	return &clusterpb.RemoveContainerResponse{}, nil
}

// grpcEngineErr maps engine sentinels onto gRPC status codes; the master's
// remote handle performs the inverse mapping.
func grpcEngineErr(err error) error {
	switch {
	case errors.Is(err, engine.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, engine.ErrConflict):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, engine.ErrNotManaged), errors.Is(err, engine.ErrImageMissing):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, engine.ErrEngineUnavailable):
		return status.Error(codes.Unavailable, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

// NewAgentServer builds the worker's gRPC server. Every connection is mTLS:
// the client must present a cluster-CA-signed cert with the master's CN —
// only the master holds the CA key, so nobody else can mint one.
func NewAgentServer(id *Identity, sampler *hostinfo.Sampler, eng engine.Engine, containers *engine.Sampler, inventory *engine.InventorySampler) *grpc.Server {
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
	clusterpb.RegisterNodeServiceServer(srv, &NodeServer{
		nodeID:     id.NodeID.String(),
		sampler:    sampler,
		eng:        eng,
		containers: containers,
		inventory:  inventory,
	})
	return srv
}

// ServeAgent runs the worker's gRPC server until ctx is canceled.
func ServeAgent(ctx context.Context, id *Identity, grpcAddr string, sampler *hostinfo.Sampler, eng engine.Engine, containers *engine.Sampler, inventory *engine.InventorySampler) error {
	lis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		return err
	}
	srv := NewAgentServer(id, sampler, eng, containers, inventory)

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
