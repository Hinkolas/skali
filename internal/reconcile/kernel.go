// Package reconcile is the level-triggered reconciliation kernel: it
// compares each environment's target revision with current observation and
// performs idempotent work toward it.
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
// subsystems (the database and bucket substrates): the environment
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
	Store              *store.Store
	Deploy             *deploy.Service
	Values             *valuestore.Service
	Journal            *journal.Service
	Registry           *module.Registry
	Observed           *observe.Store
	Source             *observe.KubeSource
	Cluster            Cluster
	LiveRouteHosts     func(context.Context, uuid.UUID) (map[string]bool, error)
	RetryCertificate   func(context.Context, kube.ObjectRef, time.Time) (bool, error)
	InspectCertificate func(context.Context, kube.ObjectRef) (*module.CertificateStatus, map[string]string, error)
	// Claims is nil without a substrate (API-only mode); database services
	// then wait visibly instead of provisioning.
	Claims ClaimManager
	// JobLogs reads the log tail of one Job's newest pod for release-command
	// failure diagnostics; nil (API-only mode, tests) skips log retrieval.
	JobLogs func(ctx context.Context, namespace, jobName string, tail int64) ([]string, error)
	// RefreshObservation bounces the observation watch connections (see
	// observe.KubeSource.Refresh). The kernel calls it when a health pass is
	// blocked on a projection the observation never delivered, the gap no
	// watch event will ever heal. Nil disables the recovery (API-only mode,
	// tests).
	RefreshObservation func()
	// HostGateway resolves the address in-cluster traffic uses to reach
	// the host machine (host.k3d.internal on the local platform); rendered
	// into intercept EndpointSlices. Nil on managed clusters, where
	// intercepts are rejected at deploy time and any stale intercept row
	// fails the desired set visibly instead of routing nowhere.
	HostGateway func(ctx context.Context) (string, error)
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
	// StorageClass names the class application volume claims request;
	// empty keeps the cluster default. The installer sets it only when
	// the cluster runs the longhorn storage driver.
	StorageClass string
	// PlatformPreference is the cluster's ordered build platform
	// preference, reported to clients through the environment status so
	// the CLI resolves single-arch builds on mixed clusters. Order
	// carries meaning; the kernel never sorts it.
	PlatformPreference []string
	// Certificates renders explicit cert-manager Certificates for TLS
	// routes and gates rollout health on their issuance. Local development
	// leaves it false: no cert-manager, HTTP-only edge.
	Certificates bool
	// RetireDrain is how long a Deployment that stopped serving (the
	// previous blue-green color, a superseded pending color, a legacy
	// workload) keeps running before it is pruned; zero means the default
	// of fifteen seconds.
	RetireDrain time.Duration
}

type Kernel struct {
	deps  Deps
	cfg   Config
	queue workqueue.TypedRateLimitingInterface[uuid.UUID]

	// retired holds, per Deployment that stopped serving, when it was first
	// seen not serving; the drain window counts from there. In-memory by
	// design: after a restart timers re-arm and a retired Deployment lives
	// one extra window at most.
	retireMu sync.Mutex
	retired  map[retireKey]time.Time
}

func New(deps Deps, cfg Config) *Kernel {
	if cfg.Workers <= 0 {
		cfg.Workers = 2
	}
	if cfg.Resync <= 0 {
		cfg.Resync = 5 * time.Minute
	}
	if cfg.Audit <= 0 {
		cfg.Audit = 30 * time.Minute
	}
	if cfg.RolloutDeadline <= 0 {
		cfg.RolloutDeadline = 10 * time.Minute
	}
	if cfg.RetireDrain <= 0 {
		cfg.RetireDrain = defaultRetireDrain
	}
	// The failure backoff is capped at the health-check cadence: a transient
	// error must never park an in-flight rollout longer than an ordinary
	// waiting pass.
	return &Kernel{
		deps:    deps,
		cfg:     cfg,
		retired: map[retireKey]time.Time{},
		queue: workqueue.NewTypedRateLimitingQueue(workqueue.NewTypedWithMaxWaitRateLimiter(
			workqueue.DefaultTypedControllerRateLimiter[uuid.UUID](), requeueHealthCheck)),
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

	auditTicker := time.NewTicker(k.cfg.Audit)
	defer auditTicker.Stop()
	resyncTicker := time.NewTicker(k.cfg.Resync)
	defer resyncTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			k.queue.ShutDown()
			workers.Wait()
			return nil
		case <-auditTicker.C:
			k.audit(ctx)
		case <-resyncTicker.C:
			k.resync(ctx)
		}
	}
}

// resync is the environment-level floor between audits: every environment
// whose cluster state has not reached its promoted target re-enters the
// queue, so a lost watch event or a dropped requeue delays convergence by at
// most one interval instead of until the audit.
func (k *Kernel) resync(ctx context.Context) {
	targets, err := k.deps.Store.ListEnvironmentsOutOfSync(ctx)
	if err != nil {
		slog.Warn("resync: list environments out of sync", "error", err)
		return
	}
	for _, target := range targets {
		k.queue.Add(target.EnvironmentID)
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
		slog.Debug("reconcile: dequeue", "environment", environmentID,
			"requeue", requeue, "error", err != nil)
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
// nothing (removal is a destructive transition that does not exist yet;
// see docs/limitations.md).
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
	Mode   string // "connected" or "api-only"
	Ready  bool
	Source module.SourceStatus
	// Sources lists every registered observation source, kubernetes first;
	// provider observers (seaweedfs) fail independently of the cluster
	// watch.
	Sources    []observe.NamedSource
	Kinds      []observe.KindSync
	QueueDepth int
	Workers    int
}

// NodePlatforms exposes the observed cluster platforms to the API layer
// without leaking the observed store.
func (k *Kernel) NodePlatforms() []string {
	return k.deps.Observed.NodePlatforms()
}

// Nodes lists the observed node records for the member-visible node
// projection, again without leaking the observed store.
func (k *Kernel) Nodes() []observe.NodeRecord {
	return k.deps.Observed.Nodes()
}

func (k *Kernel) Observation() ObservationInfo {
	info := ObservationInfo{
		Mode:       "api-only",
		Ready:      k.deps.Observed.Ready(),
		Source:     k.deps.Observed.Source(),
		Sources:    k.deps.Observed.Sources(),
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
