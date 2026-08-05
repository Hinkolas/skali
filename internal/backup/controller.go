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
	// Enqueue pokes the environment reconciler; restore uses it to stop and
	// resume the environment around data movement.
	Enqueue func(environmentID uuid.UUID)
	// Version is stamped into snapshot manifests.
	Version string
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
}

// Controller executes backup and restore rows. Create with New, start with
// Run; CreateBackup and friends are the API-facing entry points.
type Controller struct {
	deps  Deps
	cfg   Config
	queue workqueue.TypedRateLimitingInterface[uuid.UUID]
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
	return &Controller{
		deps: deps,
		cfg:  cfg,
		queue: workqueue.NewTypedRateLimitingQueue(
			workqueue.DefaultTypedControllerRateLimiter[uuid.UUID]()),
	}
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
