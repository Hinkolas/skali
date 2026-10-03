package backup

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"k8s.io/client-go/util/workqueue"

	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/deploy"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate"
	"github.com/Hinkolas/skali/internal/valuestore"
)

// Deps are the backup controller's collaborators. The controller runs
// beside the reconcile kernel and the substrate with its own queue: backup
// and restore runs are operational work driven by durable backup rows,
// never by the environment converge loop.
type Deps struct {
	Store   *store.Store
	Journal *journal.Service
	Values  *valuestore.Service
	DB      *dbstore.Service
	Deploy  *deploy.Service
	Kube    *kube.Client
	Targets *TargetStore
	// Buckets is the substrate's bucket door: platform-identity access
	// for backups and restores, and the restore fence. Nil disables bucket
	// components with a visible error.
	Buckets BucketAccess
	// Enqueue pokes the environment reconciler; restore uses it to stop and
	// resume the environment around data movement.
	Enqueue func(environmentID uuid.UUID)
	// Version is stamped into snapshot manifests.
	Version string
}

// BucketAccess is what the backup engine asks of the substrate for one
// service's bucket; *substrate.Controller implements it.
type BucketAccess interface {
	// PlatformBucketAccess resolves the bucket and the platform keypair.
	PlatformBucketAccess(ctx context.Context, environmentID uuid.UUID, serviceKey string) (substrate.BucketAccess, error)
	// FenceBucket keeps the environment's credentials away from the
	// bucket while a restore rewrites it; UnfenceBucket lets them back.
	FenceBucket(ctx context.Context, environmentID uuid.UUID, serviceKey string) error
	UnfenceBucket(ctx context.Context, environmentID uuid.UUID, serviceKey string) error
}

type Config struct {
	Workers int
	// WorkerImage overrides the image backup Jobs run the data mover in;
	// empty resolves the daemon's own Deployment image.
	WorkerImage string
	// JobTimeout bounds one backup or restore Job.
	JobTimeout time.Duration
	// ReconvergeTimeout bounds how long a restore waits for the resumed
	// revision to become healthy before the run fails (the target stays,
	// reconciliation continues).
	ReconvergeTimeout time.Duration
	// CopyConcurrency is how many objects a bucket backup or restore copies
	// at once; zero takes the default.
	CopyConcurrency int
}

// Controller executes backup and restore rows. Create with New, start with
// Run; CreateBackup and friends are the API-facing entry points.
type Controller struct {
	deps  Deps
	cfg   Config
	queue workqueue.TypedRateLimitingInterface[uuid.UUID]
	// openStore opens a bucket-scoped object store for the target; tests
	// point it at an in-memory fake.
	openStore func(s3Location) (objectStore, error)
	// openBucket opens a service's bucket as the platform identity; tests
	// point it at an in-memory fake.
	openBucket func(ctx context.Context, environmentID uuid.UUID, serviceKey string) (objectStore, string, error)
	// revisions loads revision documents; deps.Deploy in production, a
	// fake in tests.
	revisions revisionLoader
}

func New(deps Deps, cfg Config) *Controller {
	if cfg.Workers <= 0 {
		// Backups move data; one at a time per installation is deliberate.
		cfg.Workers = 1
	}
	if cfg.JobTimeout <= 0 {
		cfg.JobTimeout = time.Hour
	}
	if cfg.ReconvergeTimeout <= 0 {
		cfg.ReconvergeTimeout = 10 * time.Minute
	}
	if cfg.CopyConcurrency <= 0 {
		cfg.CopyConcurrency = defaultCopyConcurrency
	}
	c := &Controller{
		deps: deps,
		cfg:  cfg,
		queue: workqueue.NewTypedRateLimitingQueue(
			workqueue.DefaultTypedControllerRateLimiter[uuid.UUID]()),
		openStore: func(loc s3Location) (objectStore, error) { return newObjectStore(loc) },
	}
	c.openBucket = c.openServiceBucket
	if deps.Deploy != nil {
		c.revisions = deps.Deploy
	}
	return c
}

// copyOptions is how bucket copies run under this controller's settings.
func (c *Controller) copyOptions() copyOptions {
	return copyOptions{Concurrency: c.cfg.CopyConcurrency}
}

// Enqueue schedules one backup row's execution.
func (c *Controller) Enqueue(id uuid.UUID) {
	c.queue.Add(id)
}

// Run processes backup work until the context ends.
func (c *Controller) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for range c.cfg.Workers {
		wg.Go(func() {
			for c.processNext(ctx) {
			}
		})
	}
	<-ctx.Done()
	c.queue.ShutDown()
	wg.Wait()
}

func (c *Controller) processNext(ctx context.Context) bool {
	id, shutdown := c.queue.Get()
	if shutdown {
		return false
	}
	defer c.queue.Done(id)

	start := time.Now()
	if err := c.process(ctx, id); err != nil {
		slog.ErrorContext(ctx, "backup: process", "backup", id, "err", err,
			"elapsed", time.Since(start).Round(time.Millisecond))
	}
	c.queue.Forget(id)
	return true
}
