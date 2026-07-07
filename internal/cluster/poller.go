package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/clusterpb"
	"github.com/Hinkolas/skali/internal/engine"
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
// on an interval, records facts + observed containers + last_seen, and flips
// unreachable nodes offline after a staleness threshold. It also owns
// cluster hygiene (sweeping dead join tokens, pruning history and gone
// containers).
type Poller struct {
	st         *store.Store
	conns      *ConnPool
	selfID     uuid.UUID
	sampler    *hostinfo.Sampler        // the master's own node metrics
	containers *engine.Sampler          // the master's own containers; nil in tests
	inventory  *engine.InventorySampler // the master's own images/volumes; nil in tests
	interval   time.Duration
	staleAfter time.Duration
}

// NewPoller wires the poller over the shared connection pool.
// interval/staleAfter of 0 take defaults — the seam exists for tests.
func NewPoller(st *store.Store, conns *ConnPool, selfID uuid.UUID, sampler *hostinfo.Sampler, containers *engine.Sampler, inventory *engine.InventorySampler, interval, staleAfter time.Duration) (*Poller, error) {
	if interval <= 0 {
		interval = defaultPollInterval
	}
	if staleAfter <= 0 {
		staleAfter = defaultStaleAfter
	}
	return &Poller{
		st: st, conns: conns, selfID: selfID, sampler: sampler, containers: containers,
		inventory: inventory, interval: interval, staleAfter: staleAfter,
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
	// The master's own row is stamped locally — no gRPC to self. Containers
	// take the same local shortcut through the master's own sampler.
	var selfMetrics *clusterpb.NodeMetrics
	if snap, ok := p.sampler.Latest(); ok {
		selfMetrics = metricsProto(snap)
	}
	var selfContainers *clusterpb.ContainerReport
	if p.containers != nil {
		selfContainers = containerReport(p.containers)
	}
	selfInventory := inventoryReport(p.inventory) // nil-tolerant
	if err := p.recordHeartbeat(ctx, p.selfID, runtime.GOARCH, runtime.GOOS, version.Version, selfMetrics, selfContainers, selfInventory); err != nil {
		slog.WarnContext(ctx, "record self heartbeat", "err", err)
	}

	nodes, err := p.st.ListNodes(ctx)
	if err != nil {
		slog.WarnContext(ctx, "list nodes for heartbeat", "err", err)
		return
	}

	sem := make(chan struct{}, maxConcurrentPolls)
	var wg sync.WaitGroup
	ids := make(map[uuid.UUID]struct{}, len(nodes))
	for _, node := range nodes {
		ids[node.ID] = struct{}{}
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
	p.conns.Retain(ids)

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
	if _, err := p.st.PruneGoneNodeContainers(ctx); err != nil {
		slog.WarnContext(ctx, "prune gone containers", "err", err)
	}
}

// recordHeartbeat stamps a node row (facts + latest metrics), appends a
// metrics history sample, and records the container and inventory reports.
// History rows exist only for heartbeats that carried metrics — offline
// windows and cold samplers leave no rows, which the UI renders as chart
// gaps (never zero-filled).
func (p *Poller) recordHeartbeat(ctx context.Context, nodeID uuid.UUID, arch, osName, ver string, m *clusterpb.NodeMetrics, report *clusterpb.ContainerReport, inv *clusterpb.InventoryReport) error {
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
	if err := p.recordContainers(ctx, nodeID, report); err != nil {
		return err
	}
	if err := p.recordInventory(ctx, nodeID, inv); err != nil {
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

// recordContainers applies one node's container report to observed state. A
// nil report means UNKNOWN (engine unreachable, sampler cold) and must leave
// rows untouched — flipping anything to gone here would mass-expire
// containers every time an agent restarts. A present report is authoritative
// for the node: rows it omits flip to gone.
func (p *Poller) recordContainers(ctx context.Context, nodeID uuid.UUID, report *clusterpb.ContainerReport) error {
	if report == nil {
		return nil
	}
	seen := make([]string, 0, len(report.GetContainers()))
	for _, info := range report.GetContainers() {
		seen = append(seen, info.GetId())
		params, err := upsertNodeContainerParams(nodeID, info)
		if err != nil {
			// Non-conformant (e.g. hand-labeled without a valid kind): visible
			// to the operator via logs, never persisted.
			slog.DebugContext(ctx, "skip container report entry",
				"node_id", nodeID, "container_id", info.GetId(), "err", err)
			continue
		}
		if err := p.st.UpsertNodeContainer(ctx, params); err != nil {
			// FK failures on a concurrently-deleted node are harmless.
			slog.DebugContext(ctx, "upsert node container",
				"node_id", nodeID, "container_id", info.GetId(), "err", err)
		}
	}
	_, err := p.st.MarkMissingNodeContainersGone(ctx, store.MarkMissingNodeContainersGoneParams{
		NodeID:       nodeID,
		ContainerIds: seen,
	})
	return err
}

// recordInventory applies one node's image/volume report to observed state.
// A nil report means UNKNOWN (engine unreachable, sampler cold) and must
// leave rows untouched — deleting anything here would wipe the node's
// inventory every time an agent restarts. A present report is authoritative
// for the node: rows it omits are deleted (inventory mirrors reality, no
// gone breadcrumbs).
func (p *Poller) recordInventory(ctx context.Context, nodeID uuid.UUID, inv *clusterpb.InventoryReport) error {
	if inv == nil {
		return nil
	}

	imageIDs := make([]string, 0, len(inv.GetImages()))
	for _, img := range inv.GetImages() {
		imageIDs = append(imageIDs, img.GetId())
		if err := p.st.UpsertNodeImage(ctx, upsertNodeImageParams(nodeID, img)); err != nil {
			// FK failures on a concurrently-deleted node are harmless.
			slog.DebugContext(ctx, "upsert node image",
				"node_id", nodeID, "image_id", img.GetId(), "err", err)
		}
	}
	if _, err := p.st.DeleteMissingNodeImages(ctx, store.DeleteMissingNodeImagesParams{
		NodeID:   nodeID,
		ImageIds: imageIDs,
	}); err != nil {
		return err
	}

	names := make([]string, 0, len(inv.GetVolumes()))
	for _, v := range inv.GetVolumes() {
		names = append(names, v.GetName())
		if err := p.st.UpsertNodeVolume(ctx, upsertNodeVolumeParams(nodeID, v)); err != nil {
			slog.DebugContext(ctx, "upsert node volume",
				"node_id", nodeID, "name", v.GetName(), "err", err)
		}
	}
	_, err := p.st.DeleteMissingNodeVolumes(ctx, store.DeleteMissingNodeVolumesParams{
		NodeID: nodeID,
		Names:  names,
	})
	return err
}

func upsertNodeImageParams(nodeID uuid.UUID, img *clusterpb.ImageInfo) store.UpsertNodeImageParams {
	// Empty repeated fields arrive as nil, which pgx writes as SQL NULL —
	// normalize so the NOT NULL array columns hold '{}' instead.
	tags, digests := img.GetRepoTags(), img.GetRepoDigests()
	if tags == nil {
		tags = []string{}
	}
	if digests == nil {
		digests = []string{}
	}
	params := store.UpsertNodeImageParams{
		NodeID:      nodeID,
		ImageID:     img.GetId(),
		RepoTags:    tags,
		RepoDigests: digests,
		SizeBytes:   img.GetSizeBytes(),
		Dangling:    len(tags) == 0,
		Containers:  int32(img.GetContainers()),
	}
	if ts := img.GetCreatedAtUnix(); ts != 0 {
		t := time.Unix(ts, 0)
		params.ImageCreated = &t
	}
	return params
}

func upsertNodeVolumeParams(nodeID uuid.UUID, v *clusterpb.VolumeInfo) store.UpsertNodeVolumeParams {
	labels := v.GetLabels()
	if labels == nil {
		labels = map[string]string{} // unlabeled volumes are the norm; keep JSONB '{}'
	}
	raw, err := json.Marshal(labels)
	if err != nil { // unreachable for map[string]string; belt and braces
		raw = []byte("{}")
	}
	params := store.UpsertNodeVolumeParams{
		NodeID:     nodeID,
		Name:       v.GetName(),
		Driver:     v.GetDriver(),
		Scope:      v.GetScope(),
		Mountpoint: v.GetMountpoint(),
		Labels:     raw,
		Containers: int32(v.GetContainers()),
	}
	if ts := v.GetCreatedAtUnix(); ts != 0 {
		t := time.Unix(ts, 0)
		params.VolumeCreated = &t
	}
	return params
}

// upsertNodeContainerParams maps one wire container onto the observed-state
// row. The kind is extracted from the labels so the table (and everything
// reading it) can query it directly.
func upsertNodeContainerParams(nodeID uuid.UUID, info *clusterpb.ContainerInfo) (store.UpsertNodeContainerParams, error) {
	kind := info.GetLabels()[engine.LabelKind]
	if err := engine.ValidateKind(kind); err != nil {
		return store.UpsertNodeContainerParams{}, err
	}
	labels, err := json.Marshal(info.GetLabels())
	if err != nil {
		return store.UpsertNodeContainerParams{}, fmt.Errorf("cluster: marshal labels: %w", err)
	}
	exitCode := info.GetExitCode()
	params := store.UpsertNodeContainerParams{
		NodeID:       nodeID,
		ContainerID:  info.GetId(),
		Name:         info.GetName(),
		Image:        info.GetImage(),
		Kind:         kind,
		State:        info.GetState(),
		Health:       nilIfEmpty(info.GetHealth()),
		ExitCode:     &exitCode,
		RestartCount: int32(info.GetRestartCount()),
		Labels:       labels,
	}
	if ts := info.GetCreatedAtUnix(); ts != 0 {
		t := time.Unix(ts, 0)
		params.ContainerCreated = &t
	}
	if ts := info.GetStartedAtUnix(); ts != 0 {
		t := time.Unix(ts, 0)
		params.ContainerStarted = &t
	}
	if s := info.GetStats(); s != nil {
		cpu := float32(s.GetCpuPercent())
		params.CpuPct = &cpu
		params.MemUsed = i64ptr(s.GetMemoryUsedBytes())
		params.MemLimit = i64ptr(s.GetMemoryLimitBytes())
		params.NetRxRate = i64ptr(s.GetNetRxBytesPerSec())
		params.NetTxRate = i64ptr(s.GetNetTxBytesPerSec())
	}
	return params, nil
}

func i64ptr(v uint64) *int64 {
	n := int64(v)
	return &n
}

// heartbeat dials one worker over the shared pool and records its facts.
func (p *Poller) heartbeat(ctx context.Context, node store.Node) error {
	ctx, cancel := context.WithTimeout(ctx, heartbeatTimeout)
	defer cancel()

	conn, err := p.conns.Get(node)
	if err != nil {
		return err
	}

	resp, err := clusterpb.NewNodeServiceClient(conn).Heartbeat(ctx, &clusterpb.HeartbeatRequest{})
	if err != nil {
		return err
	}
	if resp.GetNodeId() != node.ID.String() {
		return fmt.Errorf("cluster: node at %s identifies as %s, expected %s",
			node.AdvertiseAddr, resp.GetNodeId(), node.ID)
	}
	return p.recordHeartbeat(ctx, node.ID,
		resp.GetArch(), resp.GetOs(), resp.GetSkalidVersion(), resp.GetMetrics(), resp.GetContainers(), resp.GetInventory())
}
