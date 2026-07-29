// Package reconcile is the level-triggered reconciliation kernel: it
// compares each environment's target revision with current observation and
// performs idempotent work toward it (REWORK_V2 sections 5.2 and 8).
// Nothing required for recovery lives only in the queue; a restart performs
// an observation sync and continues toward the same target. Runs and steps
// explain what the kernel does but never drive it: deleting every journal
// row changes no reconciliation decision.
package reconcile

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/workqueue"

	"github.com/Hinkolas/skali/internal/deploy"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/revision"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/valuestore"
)

var ErrEnvironmentNotFound = errors.New("reconcile: environment not found")

// Cluster is the mutation surface the kernel needs from the cluster client;
// kube.Client implements it, tests record against a fake.
type Cluster interface {
	Apply(ctx context.Context, obj runtime.Object, force bool) (kube.ApplyResult, error)
	Delete(ctx context.Context, ref kube.ObjectRef) (bool, error)
	DisownFields(ctx context.Context, ref kube.ObjectRef, manager string, paths ...string) error
}

// ClaimManager is the kernel's generic seam to infrastructure-claim
// subsystems (the database substrate now, buckets with R6): the environment
// pass records desired claims and consumes readiness, never claim
// mechanics. The kernel stays free of service-type fields; per-service
// health still flows through module evaluation over observed claim
// projections.
type ClaimManager interface {
	// Ensure records the revision's claims, releases the ones the promoted
	// revision no longer contains, and reports each kept claim's readiness
	// keyed by dotted service name ("databases.data").
	Ensure(ctx context.Context, in ClaimEnsureInput) ([]ClaimState, error)
	// Release starts (and re-drives) the teardown of every claim of a
	// purging environment, reporting completion and, while unfinished, what
	// is still going.
	Release(ctx context.Context, environmentID uuid.UUID) (released bool, detail []string, err error)
	// Suspend tells the substrate an environment went down while keeping
	// its data, so idle pools may hibernate (local dev).
	Suspend(ctx context.Context, environmentID uuid.UUID) error
}

// ClaimEnsureInput carries the environment identity the portable revision
// deliberately does not.
type ClaimEnsureInput struct {
	ProjectID     uuid.UUID
	EnvironmentID uuid.UUID
	Revision      *revision.Revision
}

// ClaimState is one claim's readiness for batch gating and wait steps.
type ClaimState struct {
	Service     string // dotted form, e.g. "databases.data"
	Provisioned bool
	Waiting     string // human reason while not provisioned
}

// Deps wires the kernel. Cluster and Source are nil in API-only mode (no
// cluster resolved): the kernel then idles and reports observation unknown.
type Deps struct {
	Store    *store.Store
	Deploy   *deploy.Service
	Values   *valuestore.Service
	Journal  *journal.Service
	Registry *module.Registry
	Observed *observe.Store
	Source   *observe.KubeSource
	Cluster  Cluster
	// Claims is nil without a substrate (API-only mode); database services
	// then wait visibly instead of provisioning.
	Claims ClaimManager
}

type Config struct {
	Resync          time.Duration
	Audit           time.Duration
	RolloutDeadline time.Duration
	StaleThreshold  time.Duration
	Workers         int
	// ManagedCluster pins application pods to application-capable nodes.
	// Local development leaves it false.
	ManagedCluster bool
}

type Kernel struct {
	deps  Deps
	cfg   Config
	queue workqueue.TypedRateLimitingInterface[uuid.UUID]
}

func New(deps Deps, cfg Config) *Kernel {
	if cfg.Workers <= 0 {
		cfg.Workers = 2
	}
	if cfg.Audit <= 0 {
		cfg.Audit = 30 * time.Minute
	}
	if cfg.RolloutDeadline <= 0 {
		cfg.RolloutDeadline = 10 * time.Minute
	}
	return &Kernel{
		deps:  deps,
		cfg:   cfg,
		queue: workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[uuid.UUID]()),
	}
}

// Connected reports whether a cluster is wired at all.
func (k *Kernel) Connected() bool { return k.deps.Cluster != nil }

// Enqueue implements deploy.Enqueuer and is the affected-owner entry point
// for every watch-driven change.
func (k *Kernel) Enqueue(environmentID uuid.UUID) {
	k.queue.Add(environmentID)
}

// Run starts the observation source, waits for cache readiness, then runs
// the workers, the boot audit, and the periodic audit until the context
// ends. In API-only mode it just parks: the API stays up, health reads
// unknown, and nothing reconciles.
func (k *Kernel) Run(ctx context.Context) error {
	if k.deps.Cluster == nil || k.deps.Source == nil {
		<-ctx.Done()
		return nil
	}
	go func() {
		if err := k.deps.Source.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("observation source stopped", "error", err)
		}
	}()
	// Cache readiness gates everything: no worker acts, and no health is
	// reported fresh, before the initial synchronization completes.
	for !k.deps.Observed.Ready() {
		select {
		case <-ctx.Done():
			k.queue.ShutDown()
			return nil
		case <-time.After(50 * time.Millisecond):
		}
	}

	var workers sync.WaitGroup
	for range k.cfg.Workers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			k.worker(ctx)
		}()
	}
	k.audit(ctx)

	ticker := time.NewTicker(k.cfg.Audit)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			k.queue.ShutDown()
			workers.Wait()
			return nil
		case <-ticker.C:
			k.audit(ctx)
		}
	}
}

func (k *Kernel) worker(ctx context.Context) {
	for {
		environmentID, shutdown := k.queue.Get()
		if shutdown {
			return
		}
		requeue, err := k.reconcileEnvironment(ctx, environmentID)
		k.queue.Done(environmentID)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			slog.Warn("reconcile failed", "environment", environmentID, "error", err)
			k.queue.AddRateLimited(environmentID)
		case requeue > 0:
			k.queue.Forget(environmentID)
			k.queue.AddAfter(environmentID, requeue)
		default:
			k.queue.Forget(environmentID)
		}
	}
}

// audit enqueues every environment from Postgres: the slower correctness
// backstop that catches divergence with no cluster object to fire on. It
// also surfaces orphaned managed namespaces as diagnostics and touches
// nothing (removal is a destructive transition of a later milestone).
func (k *Kernel) audit(ctx context.Context) {
	targets, err := k.deps.Store.ListEnvironmentTargets(ctx)
	if err != nil {
		slog.Warn("audit: list environment targets", "error", err)
		return
	}
	known := make(map[uuid.UUID]bool, len(targets))
	for _, target := range targets {
		known[target.EnvironmentID] = true
		k.queue.Add(target.EnvironmentID)
	}
	for _, namespace := range k.deps.Observed.ManagedNamespaces() {
		if namespace.Environment != uuid.Nil && !known[namespace.Environment] {
			slog.Warn("orphaned managed namespace retained",
				"namespace", namespace.Ref.Name, "environment", namespace.Environment)
		}
	}
}

// ObservationInfo is the structural health of the observation plane itself,
// served by the system observation endpoint.
type ObservationInfo struct {
	Mode       string // "connected" or "api-only"
	Ready      bool
	Source     module.SourceStatus
	Kinds      []observe.KindSync
	QueueDepth int
	Workers    int
}

// NodePlatforms exposes the observed cluster platforms to the API layer
// without leaking the observed store.
func (k *Kernel) NodePlatforms() []string {
	return k.deps.Observed.NodePlatforms()
}

func (k *Kernel) Observation() ObservationInfo {
	info := ObservationInfo{
		Mode:       "api-only",
		Ready:      k.deps.Observed.Ready(),
		Source:     k.deps.Observed.Source(),
		QueueDepth: k.queue.Len(),
		Workers:    k.cfg.Workers,
	}
	if k.Connected() {
		info.Mode = "connected"
		if k.deps.Source != nil {
			info.Kinds = k.deps.Source.SyncStates()
		}
	}
	return info
}
