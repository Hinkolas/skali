package cluster

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"os"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	"github.com/Hinkolas/skali/internal/clusterpb"
	"github.com/Hinkolas/skali/internal/store"
)

// NewMasterServer builds the master's cluster-plane gRPC server (enrollment
// only, for now). TLS notes:
//   - The chain is [leaf, CA] so a pin-verifying enrollment client can find
//     the CA (see pinnedTLSConfig).
//   - VerifyClientCertIfGiven, not Require: enrolling nodes have no cert yet.
//     Future authenticated RPCs on this listener must check the verified
//     chain themselves.
//
// hosts are the SANs for the listener cert (CLUSTER_ADDR's host + loopback).
func NewMasterServer(st *store.Store, ca *CA, hosts []string) (*grpc.Server, error) {
	serverCert, err := ca.IssueServerCert(append(hosts, "localhost", "127.0.0.1"))
	if err != nil {
		return nil, err
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    ca.Pool(),
	})))
	clusterpb.RegisterEnrollmentServiceServer(srv, NewEnrollServer(st, ca))
	return srv, nil
}

// EnrollServer is the master-side EnrollmentService: it admits new nodes in
// exchange for a valid one-time join token. It runs on the master's TLS
// listener and is the only RPC surface that requires no client cert.
type EnrollServer struct {
	clusterpb.UnimplementedEnrollmentServiceServer
	st *store.Store
	ca *CA
}

func NewEnrollServer(st *store.Store, ca *CA) *EnrollServer {
	return &EnrollServer{st: st, ca: ca}
}

func (s *EnrollServer) Enroll(ctx context.Context, req *clusterpb.EnrollRequest) (*clusterpb.EnrollResponse, error) {
	tokenID, secret, _, err := ParseJoinToken(req.GetToken())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "malformed join token")
	}
	if _, _, err := net.SplitHostPort(req.GetAdvertiseAddr()); err != nil {
		return nil, status.Error(codes.InvalidArgument, "advertise_addr must be host:port")
	}

	nodeID, err := uuid.NewV7()
	if err != nil {
		return nil, status.Error(codes.Internal, "generate node id")
	}
	certPEM, serialHex, err := s.ca.SignNodeCert(req.GetCsrPem(), nodeID)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid CSR")
	}

	name := req.GetHostname()
	if name == "" {
		name = "node-" + nodeID.String()[:8]
	}

	var node store.Node
	err = s.st.WithTx(ctx, func(q *store.Queries) error {
		row, err := q.GetJoinTokenByID(ctx, tokenID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrTokenUsed
		}
		if err != nil {
			return err
		}
		if subtle.ConstantTimeCompare(hashJoinSecret(secret), row.Hash) != 1 {
			return ErrInvalidToken
		}
		// Burn first, atomically: rowcount 0 means used or expired, and a
		// burned token can never admit a second node even under races.
		burned, err := q.BurnJoinToken(ctx, tokenID)
		if err != nil {
			return err
		}
		if burned == 0 {
			return ErrTokenUsed
		}
		node, err = q.CreateNode(ctx, store.CreateNodeParams{
			ID:            nodeID,
			Name:          name,
			Roles:         row.Roles,
			AdvertiseAddr: req.GetAdvertiseAddr(),
			Arch:          nilIfEmpty(req.GetArch()),
			Os:            nilIfEmpty(req.GetOs()),
			SkalidVersion: nilIfEmpty(req.GetSkalidVersion()),
			CertSerial:    &serialHex,
		})
		return err
	})
	switch {
	case errors.Is(err, ErrTokenUsed):
		return nil, status.Error(codes.PermissionDenied, "join token expired or already used")
	case errors.Is(err, ErrInvalidToken):
		return nil, status.Error(codes.PermissionDenied, "join token rejected")
	case err != nil:
		slog.ErrorContext(ctx, "enroll node", "err", err)
		return nil, status.Error(codes.Internal, "enrollment failed")
	}

	slog.InfoContext(ctx, "node enrolled",
		"node_id", node.ID, "name", node.Name, "roles", node.Roles,
		"advertise_addr", node.AdvertiseAddr)

	return &clusterpb.EnrollResponse{
		NodeId:  node.ID.String(),
		CertPem: certPEM,
		CaPem:   s.ca.CertPEM,
		Roles:   node.Roles,
	}, nil
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// hostnameOrDefault is the initial node name sent along with enrollment.
func hostnameOrDefault() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}
