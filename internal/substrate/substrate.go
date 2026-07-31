// Package substrate is the shared database substrate controller (REWORK_V2
// section 10.2): it places claims onto pools, ensures the CNPG objects and
// credentials behind them, mirrors connection outputs into environment
// namespaces, and drives claim phases. It owns everything in the
// skali-platform namespace under the skalid-platform field manager. The
// installer-owned bootstrap database in skali-system is invisible here: the
// substrate only operates on pools it created itself.
//
// The controller runs beside the reconcile kernel with its own queue and
// workers. The environment loop records desired claims and consumes outputs;
// it never touches CNPG objects or credentials.
package substrate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/workqueue"

	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
)

// Namespace is the skalid-owned platform namespace holding every substrate
// pool, tenant object, and credential Secret. The installer's uninstall
// waves already delete it between project namespaces and skali-system.
const Namespace = "skali-platform"

const (
	// DefaultEngine/DefaultMajor identify the default shared pool a
	// production installation ensures eagerly at boot.
	DefaultEngine = "postgres"
	DefaultMajor  = 17

	// Pool storage defaults; per-tenant quotas are a later policy.
	defaultPoolStorage    = int64(10) << 30 // managed clusters
	defaultDevPoolStorage = int64(1) << 30  // disposable local dev

	// requeueWait is the retry interval while a claim waits on placement,
	// pool readiness, or tenant provisioning. Watch-driven enqueues arrive
	// with the dynamic observation source; this keeps progress before then
	// and remains the backstop after.
	requeueWait = 10 * time.Second
)

// Cluster is the substrate's view of the Kubernetes API: server-side apply
// plus the reads provisioning needs. *kube.Client satisfies it through the
// KubeCluster adapter; tests substitute fakes.
type Cluster interface {
	ApplyAs(ctx context.Context, obj runtime.Object, manager string, force bool) (kube.ApplyResult, error)
	Delete(ctx context.Context, ref kube.ObjectRef) (bool, error)
	GetSecret(ctx context.Context, namespace, name string) (*corev1.Secret, error)
	GetObject(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error)
	// ProxyCIDRs derives the /32 source addresses skalid's service-proxy
	// traffic presents to pods, the object-store fence's admit list.
	ProxyCIDRs(ctx context.Context) ([]string, error)
}

// KubeCluster adapts *kube.Client to the Cluster interface.
type KubeCluster struct {
	Client *kube.Client
}

func (k KubeCluster) ApplyAs(ctx context.Context, obj runtime.Object, manager string, force bool) (kube.ApplyResult, error) {
	return k.Client.ApplyAs(ctx, obj, manager, force)
}

func (k KubeCluster) Delete(ctx context.Context, ref kube.ObjectRef) (bool, error) {
	return k.Client.Delete(ctx, ref)
}

func (k KubeCluster) GetSecret(ctx context.Context, namespace, name string) (*corev1.Secret, error) {
	return k.Client.Clientset.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
}

func (k KubeCluster) GetObject(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	return k.Client.Dynamic.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
}

func (k KubeCluster) ProxyCIDRs(ctx context.Context) ([]string, error) {
	return k.Client.NodeProxyCIDRs(ctx)
}

type Deps struct {
	DB       *dbstore.Service
	Cluster  Cluster
	Observed *observe.Store
	// Seaweed is the object store's admin client; nil disables bucket
	// provisioning with a visible waiting reason.
	Seaweed *seaweed.Client
	// Enqueue pokes the environment reconciler when a claim's outputs become
	// ready or its health-relevant state changes. Optional until the kernel
	// consumes claims.
	Enqueue func(environmentID uuid.UUID)
}

type Config struct {
	// Managed reports a production installation: capability node selectors
	// apply, tiers derive from database-capable node counts, and the default
	// shared pool is ensured eagerly at boot. Local dev leaves this false:
	// every claim collapses onto the single dev pool and pools start lazily.
	Managed bool
	// Capabilities is the installation's declared capability set; the eager
	// boot ensure runs only when it includes the database capability.
	Capabilities []string
	// S3Domain is the optional public S3 endpoint domain: bucket endpoints
	// publish on it and the substrate renders the S3 ingress. Empty keeps
	// bucket access in-cluster.
	S3Domain string
	// Resync re-enqueues unsettled claims and live pools periodically as the
	// audit backstop.
	Resync  time.Duration
	Workers int
}

type workKind string

const (
	workClaim  workKind = "claim"
	workPool   workKind = "pool"
	workBoot   workKind = "boot"
	workBucket workKind = "bucket-claim"
	workStore  workKind = "object-store"
)

type workKey struct {
	kind workKind
	id   uuid.UUID
}

// Controller drives the substrate. Create with New, start with Run.
type Controller struct {
	deps  Deps
	cfg   Config
	queue workqueue.TypedRateLimitingInterface[workKey]

	mu        sync.Mutex
	waiting   map[uuid.UUID]string // claim id -> current waiting reason
	probePoke func()               // provider observer re-poll, set by SetProbePoke
}

// SetProbePoke wires the provider observer's coalesced re-poll; the
// controller pokes it after every bucket mutation so usage and existence
// converge without waiting a full poll interval.
func (c *Controller) SetProbePoke(poke func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.probePoke = poke
}

func (c *Controller) pokeProbe() {
	c.mu.Lock()
	poke := c.probePoke
	c.mu.Unlock()
	if poke != nil {
		poke()
	}
}

func New(deps Deps, cfg Config) *Controller {
	if cfg.Workers <= 0 {
		cfg.Workers = 2
	}
	if cfg.Resync <= 0 {
		cfg.Resync = 5 * time.Minute
	}
	// The failure backoff is capped at the waiting cadence: a transient
	// error must never park in-flight claim work longer than an ordinary
	// waiting pass.
	return &Controller{
		deps: deps,
		cfg:  cfg,
		queue: workqueue.NewTypedRateLimitingQueue(workqueue.NewTypedWithMaxWaitRateLimiter(
			workqueue.DefaultTypedControllerRateLimiter[workKey](), requeueWait)),
		waiting: make(map[uuid.UUID]string),
	}
}

// EnqueueClaim schedules one claim's reconciliation.
func (c *Controller) EnqueueClaim(id uuid.UUID) {
	c.queue.Add(workKey{kind: workClaim, id: id})
}

// EnqueuePool schedules one pool's reconciliation.
func (c *Controller) EnqueuePool(id uuid.UUID) {
	c.queue.Add(workKey{kind: workPool, id: id})
}

// EnqueueBucketClaim schedules one bucket claim's reconciliation.
func (c *Controller) EnqueueBucketClaim(id uuid.UUID) {
	c.queue.Add(workKey{kind: workBucket, id: id})
}

// EnqueueObjectStore schedules the physical system's reconciliation; with
// one live store per installation the key carries no id.
func (c *Controller) EnqueueObjectStore() {
	c.queue.Add(workKey{kind: workStore})
}

// WaitingReason reports why a claim is not progressing, empty when it is.
func (c *Controller) WaitingReason(claimID uuid.UUID) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.waiting[claimID]
}

func (c *Controller) setWaiting(claimID uuid.UUID, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if reason == "" {
		delete(c.waiting, claimID)
		return
	}
	c.waiting[claimID] = reason
}

// Run processes substrate work until the context ends.
func (c *Controller) Run(ctx context.Context) {
	defer c.queue.ShutDown()

	c.queue.Add(workKey{kind: workBoot})
	c.resyncEnqueue(ctx)

	var wg sync.WaitGroup
	for range c.cfg.Workers {
		wg.Go(func() {
			for c.processNext(ctx) {
			}
		})
	}

	ticker := time.NewTicker(c.cfg.Resync)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			c.queue.ShutDown()
			wg.Wait()
			return
		case <-ticker.C:
			c.resyncEnqueue(ctx)
		}
	}
}

func (c *Controller) processNext(ctx context.Context) bool {
	key, shutdown := c.queue.Get()
	if shutdown {
		return false
	}
	defer c.queue.Done(key)

	requeue, err := c.process(ctx, key)
	switch {
	case ctx.Err() != nil:
		return false
	case err != nil:
		slog.Warn("substrate: reconcile failed", "kind", key.kind, "id", key.id, "error", err)
		c.queue.AddRateLimited(key)
	case requeue > 0:
		c.queue.Forget(key)
		c.queue.AddAfter(key, requeue)
	default:
		c.queue.Forget(key)
	}
	return true
}

func (c *Controller) process(ctx context.Context, key workKey) (time.Duration, error) {
	switch key.kind {
	case workBoot:
		return c.boot(ctx)
	case workClaim:
		return c.reconcileClaim(ctx, key.id)
	case workPool:
		return c.reconcilePool(ctx, key.id)
	case workBucket:
		return c.reconcileBucketClaim(ctx, key.id)
	case workStore:
		return c.reconcileObjectStore(ctx)
	}
	return 0, nil
}

// boot ensures the platform namespace, eagerly ensures the default shared
// pool on database-capable production installations, and enqueues all
// outstanding work.
func (c *Controller) boot(ctx context.Context) (time.Duration, error) {
	if err := c.ensureNamespace(ctx); err != nil {
		return 0, err
	}
	// Rebuild the observed claim projections before any evaluation trusts
	// them; the resync pass then drives outstanding work.
	if err := c.publishLiveClaims(ctx); err != nil {
		return 0, err
	}
	if hasCapability(c.cfg.Capabilities, layout.CapabilityDatabase) {
		if requeue, err := c.ensureDefaultSharedPool(ctx); err != nil || requeue > 0 {
			return requeue, err
		}
	}
	// The object store comes up eagerly wherever the capability exists
	// (owner decision 2026-07-31: the dev substrate is always on); an
	// existing store row resumes its reconciliation here either way.
	if hasCapability(c.cfg.Capabilities, layout.CapabilityObjectStorage) {
		c.EnqueueObjectStore()
	} else if _, err := c.deps.DB.LiveObjectStore(ctx); err == nil {
		c.EnqueueObjectStore()
	}
	return 0, nil
}

func (c *Controller) resyncEnqueue(ctx context.Context) {
	claims, err := c.deps.DB.ListUnsettledClaims(ctx)
	if err != nil {
		slog.Warn("substrate: list unsettled claims", "error", err)
	}
	for _, row := range claims {
		c.EnqueueClaim(row.ID)
	}
	pools, err := c.deps.DB.ListLiveClusters(ctx)
	if err != nil {
		slog.Warn("substrate: list live pools", "error", err)
	}
	for _, pool := range pools {
		c.EnqueuePool(pool.ID)
	}
	buckets, err := c.deps.DB.ListUnsettledBucketClaims(ctx)
	if err != nil {
		slog.Warn("substrate: list unsettled bucket claims", "error", err)
	}
	for _, row := range buckets {
		c.EnqueueBucketClaim(row.ID)
	}
	if _, err := c.deps.DB.LiveObjectStore(ctx); err == nil {
		c.EnqueueObjectStore()
	} else if !errors.Is(err, dbstore.ErrNotFound) {
		slog.Warn("substrate: live object store", "error", err)
	}
}

func (c *Controller) ensureNamespace(ctx context.Context) error {
	namespace := &corev1.Namespace{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
		ObjectMeta: metav1.ObjectMeta{
			Name: Namespace,
			// The system label keeps the namespace out of environment
			// semantics; the managed label is deliberately absent so the
			// environment-facing informers never index platform objects as
			// project state.
			Labels: map[string]string{"skali.dev/system": "true"},
		},
	}
	if _, err := c.deps.Cluster.ApplyAs(ctx, namespace, kube.FieldManagerPlatform, false); err != nil {
		return fmt.Errorf("substrate: ensure namespace: %w", err)
	}
	return nil
}

// ensureDefaultSharedPool creates the default shared pool row when database
// capability exists; readiness of the CNPG objects follows through the
// normal pool work path.
func (c *Controller) ensureDefaultSharedPool(ctx context.Context) (time.Duration, error) {
	_, err := c.deps.DB.LiveSharedCluster(ctx, DefaultEngine, DefaultMajor)
	if err == nil {
		return 0, nil
	}
	if !errors.Is(err, dbstore.ErrNotFound) {
		return 0, err
	}
	// Dev counts its single node without capability labels (the same
	// derivation claims use); managed installations size from observation.
	capable := c.capableNodes()
	if capable == 0 {
		// Observation has not synced yet (or no database nodes joined);
		// retry rather than sizing the pool wrong.
		return requeueWait, nil
	}
	tier := layout.DeriveTier(capable)
	if !c.cfg.Managed {
		tier = layout.TierSingle
	}
	pool, err := c.createPool(ctx, poolPlan{
		engine: DefaultEngine,
		major:  DefaultMajor,
		class:  dbstore.ClassShared,
		name:   sharedPoolName(DefaultEngine, DefaultMajor),
		tier:   tier,
	})
	if err != nil {
		return 0, err
	}
	slog.Info("substrate: default shared pool created", "pool", pool.Name, "instances", pool.Instances)
	c.EnqueuePool(pool.ID)
	return 0, nil
}

// reconcilePool re-applies one pool's desired objects; claims drive tenant
// work separately.
func (c *Controller) reconcilePool(ctx context.Context, id uuid.UUID) (time.Duration, error) {
	pool, err := c.deps.DB.GetCluster(ctx, id)
	if err != nil {
		if errors.Is(err, dbstore.ErrNotFound) {
			return 0, nil
		}
		return 0, err
	}
	switch pool.State {
	case dbstore.StateReleased:
		return 0, nil
	case dbstore.StateReleasing:
		return 0, c.releasePool(ctx, *pool)
	}
	if err := c.ensurePool(ctx, *pool); err != nil {
		return 0, err
	}
	return 0, nil
}

func hasCapability(capabilities []string, capability string) bool {
	return slices.Contains(capabilities, capability)
}

// ownerNames splits a service claim's denormalized owner ref
// ("project/<p>/environment/<e>/service/<k>"; names cannot contain slashes)
// back into the names that address its environment namespace and output
// Secret.
func ownerNames(ownerRef string) (project, environment, service string, ok bool) {
	parts := strings.Split(ownerRef, "/")
	if len(parts) != 6 || parts[0] != "project" || parts[2] != "environment" || parts[4] != "service" {
		return "", "", "", false
	}
	return parts[1], parts[3], parts[5], true
}
