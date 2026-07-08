// Package reconcile is the master-side workload orchestrator: desired state
// (workloads + reconciler-owned assignments) converged onto nodes through
// the imperative NodeHandle seam. It is level-triggered like the rest of the
// cluster plane — every pass recomputes intent from the rows and the
// observed state, so a lost poke, a crashed master, or a leader failover
// costs latency, never correctness. Nothing the reconciler needs to resume
// lives in memory.
package reconcile

import (
	"context"
	"encoding/json"
	"log/slog"
	"math/rand/v2"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/cluster"
	"github.com/Hinkolas/skali/internal/engine"
	"github.com/Hinkolas/skali/internal/store"
)

const (
	// defaultInterval is the level-triggered baseline; pokes accelerate.
	defaultInterval = 15 * time.Second

	// maxConcurrentWorkloads bounds parallel convergence goroutines.
	maxConcurrentWorkloads = 4

	// imageResolveTimeout bounds one import attempt (matches the API's
	// import ceiling).
	imageResolveTimeout = 15 * time.Minute

	backoffMin = 5 * time.Second
	backoffMax = 5 * time.Minute
)

// Importer is the mirror surface the image phase drives; *mirror.Importer
// implements it.
type Importer interface {
	Import(ctx context.Context, reference string) (store.RegistryImage, error)
}

// Reconciler converges workloads. One pass per tick or poke; each workload
// converges in its own goroutine under an in-flight guard, so one slow
// import never stalls the others or the next pass.
type Reconciler struct {
	st        *store.Store
	handleFor func(store.Node) cluster.NodeHandle
	importer  Importer
	endpoint  string // mirror host:port for materialized image refs
	interval  time.Duration
	poke      chan struct{}

	mu       sync.Mutex
	inflight map[uuid.UUID]bool
	sem      chan struct{}
}

// NewReconciler wires the loop. endpoint is the mirror's host:port — the
// registry gate (CLUSTER_ADDR) has already been checked by the caller.
func NewReconciler(st *store.Store, handleFor func(store.Node) cluster.NodeHandle, importer Importer, endpoint string) *Reconciler {
	return &Reconciler{
		st:        st,
		handleFor: handleFor,
		importer:  importer,
		endpoint:  endpoint,
		interval:  defaultInterval,
		poke:      make(chan struct{}, 1),
		inflight:  make(map[uuid.UUID]bool),
		sem:       make(chan struct{}, maxConcurrentWorkloads),
	}
}

// Poke requests a prompt pass; coalescing and non-blocking, like every
// doorbell in the system.
func (r *Reconciler) Poke() {
	select {
	case r.poke <- struct{}{}:
	default:
	}
}

// Run drives passes until ctx ends.
func (r *Reconciler) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	r.pass(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.poke:
		case <-ticker.C:
		}
		r.pass(ctx)
	}
}

// observedContainer is one node_containers row that claims workload
// ownership, with its identity labels decoded.
type observedContainer struct {
	row        store.NodeContainer
	labels     map[string]string
	workloadID uuid.UUID
	ordinal    int32
	hash       string
}

// pass reads the cluster once and fans convergence out per workload.
// Convergence goroutines outlive the pass; the in-flight guard keeps a
// workload single-writer across passes.
func (r *Reconciler) pass(ctx context.Context) {
	workloads, err := r.st.ListWorkloads(ctx)
	if err != nil {
		slog.WarnContext(ctx, "reconcile: list workloads", "err", err)
		return
	}
	assignments, err := r.st.ListWorkloadAssignments(ctx)
	if err != nil {
		slog.WarnContext(ctx, "reconcile: list assignments", "err", err)
		return
	}
	nodes, err := r.st.ListNodes(ctx)
	if err != nil {
		slog.WarnContext(ctx, "reconcile: list nodes", "err", err)
		return
	}
	observed, err := r.observedContainers(ctx)
	if err != nil {
		slog.WarnContext(ctx, "reconcile: list observed containers", "err", err)
		return
	}

	nodeByID := make(map[uuid.UUID]store.Node, len(nodes))
	for _, n := range nodes {
		nodeByID[n.ID] = n
	}
	// Placement spreading input: how many slots each node already holds.
	slotCount := make(map[uuid.UUID]int)
	for _, a := range assignments {
		if a.NodeID != nil {
			slotCount[*a.NodeID]++
		}
	}
	obsByWorkload := make(map[uuid.UUID][]observedContainer)
	for _, o := range observed {
		obsByWorkload[o.workloadID] = append(obsByWorkload[o.workloadID], o)
	}

	for _, row := range workloads {
		if !r.acquire(row.ID) {
			continue
		}
		w, err := decodeWorkload(row)
		if err != nil {
			// A row that cannot decode is a bug, not a retry candidate.
			slog.ErrorContext(ctx, "reconcile: undecodable workload", "workload", row.Name, "err", err)
			r.release(row.ID)
			continue
		}
		obs := obsByWorkload[row.ID]
		go func() {
			defer r.release(w.ID)
			r.converge(ctx, w, obs, nodeByID, slotCount)
		}()
	}

	// Orphan GC. Removal needs an explicit contradiction, never mere absence
	// of bookkeeping: a slot whose assignment row was lost is adoption
	// material for the planner, not garbage. Orphans are containers whose
	// workload no longer exists (post-teardown stragglers), whose slot
	// verifiably lives on another node (relocation leftovers), or whose
	// ordinal exceeds the replica count with no row left (post-crash
	// scale-down remains). Decisions come only from observed rows, which
	// persist while a node is offline: silence never triggers removals.
	wlByID := make(map[uuid.UUID]store.Workload, len(workloads))
	for _, row := range workloads {
		wlByID[row.ID] = row
	}
	type slot struct {
		wl      uuid.UUID
		ordinal int32
	}
	asgNode := make(map[slot]*uuid.UUID, len(assignments))
	for _, a := range assignments {
		asgNode[slot{a.WorkloadID, a.Ordinal}] = a.NodeID
	}
	var orphans []observedContainer
	for _, o := range observed {
		wl, exists := wlByID[o.workloadID]
		if !exists {
			orphans = append(orphans, o)
			continue
		}
		nodeID, hasRow := asgNode[slot{o.workloadID, o.ordinal}]
		switch {
		case hasRow && nodeID != nil && *nodeID != o.row.NodeID:
			orphans = append(orphans, o)
		case !hasRow && o.ordinal >= wl.Replicas && wl.DesiredState != DesiredDeleting:
			orphans = append(orphans, o)
		}
	}
	if len(orphans) > 0 && r.acquire(uuid.Nil) {
		go func() {
			defer r.release(uuid.Nil)
			r.removeOrphans(ctx, orphans, nodeByID)
		}()
	}
}

func (r *Reconciler) observedContainers(ctx context.Context) ([]observedContainer, error) {
	rows, err := r.st.ListWorkloadObservedContainers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]observedContainer, 0, len(rows))
	for _, row := range rows {
		var labels map[string]string
		if err := json.Unmarshal(row.Labels, &labels); err != nil {
			continue
		}
		wlID, err := uuid.Parse(labels[engine.LabelWorkload])
		if err != nil {
			continue // foreign label squatter; never ours to touch
		}
		ordinal, err := strconv.Atoi(labels[engine.LabelInstance])
		if err != nil || ordinal < 0 {
			continue
		}
		out = append(out, observedContainer{
			row: row, labels: labels, workloadID: wlID,
			ordinal: int32(ordinal), hash: labels[engine.LabelConfigHash],
		})
	}
	return out, nil
}

func (r *Reconciler) acquire(id uuid.UUID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inflight[id] {
		return false
	}
	select {
	case r.sem <- struct{}{}:
	default:
		return false // at the concurrency cap; the next pass retries
	}
	r.inflight[id] = true
	return true
}

func (r *Reconciler) release(id uuid.UUID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.inflight, id)
	<-r.sem
}

// idle reports whether no convergence is in flight (tests synchronize on it).
func (r *Reconciler) idle() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.inflight) == 0
}

// removeOrphans force-removes containers no assignment wants. Offline nodes
// simply fail the RPC and the next pass retries — the observed row survives
// until a node report actually drops it.
func (r *Reconciler) removeOrphans(ctx context.Context, orphans []observedContainer, nodes map[uuid.UUID]store.Node) {
	for _, o := range orphans {
		node, ok := nodes[o.row.NodeID]
		if !ok || node.Status != "online" {
			continue
		}
		if err := r.handleFor(node).RemoveContainer(ctx, o.row.ContainerID, true); err != nil {
			slog.DebugContext(ctx, "reconcile: remove orphan", "container", o.row.Name,
				"node", node.Name, "err", err)
			continue
		}
		if _, err := r.st.DeleteNodeContainer(ctx, store.DeleteNodeContainerParams{
			NodeID: o.row.NodeID, ContainerID: o.row.ContainerID,
		}); err != nil {
			slog.WarnContext(ctx, "reconcile: drop orphan row", "container", o.row.Name, "err", err)
		}
		slog.InfoContext(ctx, "reconcile: removed orphaned container",
			"container", o.row.Name, "node", node.Name)
	}
}

// backoffAfter schedules the next attempt: 5s·2^retries capped at 5m, plus
// jitter.
func backoffAfter(retries int32) time.Time {
	d := backoffMax
	if retries < 10 {
		if computed := backoffMin << retries; computed < backoffMax {
			d = computed
		}
	}
	return time.Now().Add(d + rand.N(d/4))
}
