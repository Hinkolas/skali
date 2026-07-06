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
	"github.com/Hinkolas/skali/internal/hostinfo"
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
	sampler    *hostinfo.Sampler // the master's own node metrics
	interval   time.Duration
	staleAfter time.Duration
}

// NewPoller mints the master's dialing identity from the CA (in memory; the
// master holds the CA key anyway). interval/staleAfter of 0 take defaults —
// the seam exists for tests.
func NewPoller(st *store.Store, ca *CA, selfID uuid.UUID, sampler *hostinfo.Sampler, interval, staleAfter time.Duration) (*Poller, error) {
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
		st: st, ca: ca, clientCert: clientCert, selfID: selfID, sampler: sampler,
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
	var selfMetrics *clusterpb.NodeMetrics
	if snap, ok := p.sampler.Latest(); ok {
		selfMetrics = metricsProto(snap)
	}
	if err := p.recordHeartbeat(ctx, p.selfID, runtime.GOARCH, runtime.GOOS, version.Version, selfMetrics); err != nil {
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
	if _, err := p.st.PruneNodeMetrics(ctx); err != nil {
		slog.WarnContext(ctx, "prune node metrics", "err", err)
	}
}

// recordHeartbeat stamps a node row (facts + latest metrics) and appends a
// history sample. History rows exist only for heartbeats that carried
// metrics — offline windows and cold samplers leave no rows, which the UI
// renders as chart gaps (never zero-filled).
func (p *Poller) recordHeartbeat(ctx context.Context, nodeID uuid.UUID, arch, osName, ver string, m *clusterpb.NodeMetrics) error {
	params := store.RecordNodeHeartbeatParams{
		ID:            nodeID,
		Arch:          nilIfEmpty(arch),
		Os:            nilIfEmpty(osName),
		SkalidVersion: nilIfEmpty(ver),
	}
	if m != nil {
		cpu, load := float32(m.GetCpuPercent()), float32(m.GetLoad1())
		params.CpuPct = &cpu
		params.Load1 = &load
		params.MemUsed = i64ptr(m.GetMemoryUsedBytes())
		params.MemTotal = i64ptr(m.GetMemoryTotalBytes())
		params.DiskUsed = i64ptr(m.GetDiskUsedBytes())
		params.DiskTotal = i64ptr(m.GetDiskTotalBytes())
		params.NetRxRate = i64ptr(m.GetNetRxBytesPerSec())
		params.NetTxRate = i64ptr(m.GetNetTxBytesPerSec())
		params.DiskReadRate = i64ptr(m.GetDiskReadBytesPerSec())
		params.DiskWriteRate = i64ptr(m.GetDiskWriteBytesPerSec())
	}
	if err := p.st.RecordNodeHeartbeat(ctx, params); err != nil {
		return err
	}
	if m == nil {
		return nil
	}
	return p.st.InsertNodeMetrics(ctx, store.InsertNodeMetricsParams{
		NodeID:        nodeID,
		CpuPct:        float32(m.GetCpuPercent()),
		MemUsed:       int64(m.GetMemoryUsedBytes()),
		MemTotal:      int64(m.GetMemoryTotalBytes()),
		DiskUsed:      int64(m.GetDiskUsedBytes()),
		DiskTotal:     int64(m.GetDiskTotalBytes()),
		NetRxRate:     int64(m.GetNetRxBytesPerSec()),
		NetTxRate:     int64(m.GetNetTxBytesPerSec()),
		DiskReadRate:  int64(m.GetDiskReadBytesPerSec()),
		DiskWriteRate: int64(m.GetDiskWriteBytesPerSec()),
		Load1:         float32(m.GetLoad1()),
	})
}

func i64ptr(v uint64) *int64 {
	n := int64(v)
	return &n
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
	return p.recordHeartbeat(ctx, node.ID,
		resp.GetArch(), resp.GetOs(), resp.GetSkalidVersion(), resp.GetMetrics())
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
