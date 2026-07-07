package cluster

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/clusterpb"
	"github.com/Hinkolas/skali/internal/store"
)

const (
	// watchReconcileInterval is how often the watcher diffs its streams
	// against the node list. Deliberately its own ticker, not a poller hook:
	// the two loops stay independently testable and neither can stall the
	// other.
	watchReconcileInterval = 15 * time.Second

	watchBackoffMin = time.Second
	watchBackoffMax = 30 * time.Second
)

// Watcher keeps one WatchEvents stream open to every remote node and turns
// each doorbell ring into a poke — an immediate single-node resync through
// the poller. Streams ride the shared ConnPool; a dead stream is retried
// with jittered backoff, and a node whose events never arrive still gets
// polled on the interval (the doorbell is an accelerator, not a dependency).
//
// Shutdown: Run's ctx must be canceled before the ConnPool closes (in
// runServe the loop cancel is deferred after conns.Close, and defers are
// LIFO), so streams die by cancellation, not by yanked connections.
type Watcher struct {
	st     *store.Store
	conns  *ConnPool
	selfID uuid.UUID
	poke   func(uuid.UUID)

	mu      sync.Mutex
	cancels map[uuid.UUID]context.CancelFunc
}

// NewWatcher wires the watcher; poke is typically Poller.Poke. The master's
// own node is excluded — its notifier is consumed locally, no gRPC to self.
func NewWatcher(st *store.Store, conns *ConnPool, selfID uuid.UUID, poke func(uuid.UUID)) *Watcher {
	return &Watcher{st: st, conns: conns, selfID: selfID, poke: poke,
		cancels: map[uuid.UUID]context.CancelFunc{}}
}

// Run reconciles per-node watch goroutines against the node list until ctx
// is canceled.
func (w *Watcher) Run(ctx context.Context) {
	ticker := time.NewTicker(watchReconcileInterval)
	defer ticker.Stop()
	for {
		w.reconcile(ctx)
		select {
		case <-ctx.Done():
			w.reconcileTo(nil)
			return
		case <-ticker.C:
		}
	}
}

func (w *Watcher) reconcile(ctx context.Context) {
	nodes, err := w.st.ListNodes(ctx)
	if err != nil {
		slog.WarnContext(ctx, "list nodes for watch", "err", err)
		return
	}
	want := make(map[uuid.UUID]struct{}, len(nodes))
	for _, n := range nodes {
		if n.ID == w.selfID || n.AdvertiseAddr == "" {
			continue
		}
		want[n.ID] = struct{}{}
	}
	w.reconcileTo(want)

	w.mu.Lock()
	defer w.mu.Unlock()
	for id := range want {
		if _, ok := w.cancels[id]; ok {
			continue
		}
		nodeCtx, cancel := context.WithCancel(ctx)
		w.cancels[id] = cancel
		go w.watchNode(nodeCtx, id)
	}
}

// reconcileTo cancels watchers for nodes not in want (nil = all).
func (w *Watcher) reconcileTo(want map[uuid.UUID]struct{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for id, cancel := range w.cancels {
		if _, ok := want[id]; !ok {
			cancel()
			delete(w.cancels, id)
		}
	}
}

// watchNode holds one node's stream open, poking on every ring. Each attempt
// re-fetches the node row so address or cert changes flow through the pool's
// re-dial logic.
func (w *Watcher) watchNode(ctx context.Context, id uuid.UUID) {
	backoff := watchBackoffMin
	for ctx.Err() == nil {
		rang, err := w.watchOnce(ctx, id)
		if ctx.Err() != nil {
			return
		}
		if rang {
			backoff = watchBackoffMin
		}
		slog.DebugContext(ctx, "watch stream ended", "node_id", id, "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff + rand.N(backoff/2)):
		}
		backoff = min(backoff*2, watchBackoffMax)
	}
}

// watchOnce runs one stream until it dies, reporting whether any ring
// arrived (the backoff-reset signal).
func (w *Watcher) watchOnce(ctx context.Context, id uuid.UUID) (rang bool, err error) {
	node, err := w.st.GetNodeByID(ctx, id)
	if err != nil {
		return false, err
	}
	conn, err := w.conns.Get(node)
	if err != nil {
		return false, err
	}
	stream, err := clusterpb.NewNodeServiceClient(conn).WatchEvents(ctx, &clusterpb.WatchEventsRequest{})
	if err != nil {
		return false, err
	}
	for {
		if _, err := stream.Recv(); err != nil {
			return rang, err
		}
		rang = true
		w.poke(id)
	}
}
