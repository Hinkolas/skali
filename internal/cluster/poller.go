package cluster

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/Hinkolas/skali/internal/clusterpb"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/version"
)

const (
	defaultPollInterval = 15 * time.Second
	// defaultStaleAfter is 3 intervals: a single failed tick never flaps a
	// node offline.
	defaultStaleAfter  = 45 * time.Second
	heartbeatTimeout   = 5 * time.Second
	maxConcurrentPolls = 8
)

// Poller is the master's heartbeat loop: it dials every worker's NodeService
// on an interval, records facts + last_seen, and flips unreachable nodes
// offline after a staleness threshold. It also owns cluster hygiene
// (sweeping dead join tokens).
type Poller struct {
	st         *store.Store
	ca         *CA
	clientCert tls.Certificate
	selfID     uuid.UUID
	interval   time.Duration
	staleAfter time.Duration
}

// NewPoller mints the master's dialing identity from the CA (in memory; the
// master holds the CA key anyway). interval/staleAfter of 0 take defaults —
// the seam exists for tests.
func NewPoller(st *store.Store, ca *CA, selfID uuid.UUID, interval, staleAfter time.Duration) (*Poller, error) {
	clientCert, err := ca.IssueClientCert()
	if err != nil {
		return nil, err
	}
	if interval <= 0 {
		interval = defaultPollInterval
	}
	if staleAfter <= 0 {
		staleAfter = defaultStaleAfter
	}
	return &Poller{
		st: st, ca: ca, clientCert: clientCert, selfID: selfID,
		interval: interval, staleAfter: staleAfter,
	}, nil
}

// Run ticks until ctx is canceled. One immediate tick first, so nodes show
// fresh status right after boot instead of after a full interval.
func (p *Poller) Run(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		p.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (p *Poller) tick(ctx context.Context) {
	// The master's own row is stamped locally — no gRPC to self.
	arch, os_, ver := runtime.GOARCH, runtime.GOOS, version.Version
	if err := p.st.RecordNodeHeartbeat(ctx, store.RecordNodeHeartbeatParams{
		ID: p.selfID, Arch: &arch, Os: &os_, SkalidVersion: &ver,
	}); err != nil {
		slog.WarnContext(ctx, "record self heartbeat", "err", err)
	}

	nodes, err := p.st.ListNodes(ctx)
	if err != nil {
		slog.WarnContext(ctx, "list nodes for heartbeat", "err", err)
		return
	}

	sem := make(chan struct{}, maxConcurrentPolls)
	var wg sync.WaitGroup
	for _, node := range nodes {
		if node.ID == p.selfID || node.AdvertiseAddr == "" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := p.heartbeat(ctx, node); err != nil {
				slog.DebugContext(ctx, "heartbeat failed",
					"node_id", node.ID, "addr", node.AdvertiseAddr, "err", err)
			}
		}()
	}
	wg.Wait()

	cutoff := time.Now().Add(-p.staleAfter)
	if n, err := p.st.MarkStaleNodesOffline(ctx, &cutoff); err != nil {
		slog.WarnContext(ctx, "mark stale nodes offline", "err", err)
	} else if n > 0 {
		slog.InfoContext(ctx, "nodes went offline", "count", n)
	}

	if _, err := p.st.SweepExpiredJoinTokens(ctx); err != nil {
		slog.WarnContext(ctx, "sweep join tokens", "err", err)
	}
}

// heartbeat dials one worker and records its facts. Connections are per-tick
// for now — a cached-connection pool is a later optimization once RPC volume
// grows beyond one heartbeat per interval.
func (p *Poller) heartbeat(ctx context.Context, node store.Node) error {
	ctx, cancel := context.WithTimeout(ctx, heartbeatTimeout)
	defer cancel()

	conn, err := grpc.NewClient(node.AdvertiseAddr,
		grpc.WithTransportCredentials(credentials.NewTLS(p.dialTLS(node))))
	if err != nil {
		return err
	}
	defer conn.Close()

	resp, err := clusterpb.NewNodeServiceClient(conn).Heartbeat(ctx, &clusterpb.HeartbeatRequest{})
	if err != nil {
		return err
	}
	if resp.GetNodeId() != node.ID.String() {
		return fmt.Errorf("cluster: node at %s identifies as %s, expected %s",
			node.AdvertiseAddr, resp.GetNodeId(), node.ID)
	}
	return p.st.RecordNodeHeartbeat(ctx, store.RecordNodeHeartbeatParams{
		ID:            node.ID,
		Arch:          nilIfEmpty(resp.GetArch()),
		Os:            nilIfEmpty(resp.GetOs()),
		SkalidVersion: nilIfEmpty(resp.GetSkalidVersion()),
	})
}

// dialTLS authenticates a specific worker: its cert must chain to the cluster
// CA, carry the node's UUID as SAN (standard hostname verification via
// ServerName), and match the serial recorded at enrollment — a deleted or
// re-enrolled node's old cert is refused even though the CA once signed it.
func (p *Poller) dialTLS(node store.Node) *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{p.clientCert},
		RootCAs:      p.ca.Pool(),
		ServerName:   node.ID.String(),
		VerifyPeerCertificate: func(_ [][]byte, chains [][]*x509.Certificate) error {
			if len(chains) == 0 || len(chains[0]) == 0 {
				return fmt.Errorf("cluster: no verified chain")
			}
			serial := chains[0][0].SerialNumber.Text(16)
			if node.CertSerial == nil || serial != *node.CertSerial {
				return fmt.Errorf("cluster: node %s presented unexpected cert serial", node.ID)
			}
			return nil
		},
	}
}
