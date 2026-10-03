// Package rotation drives managed credential rotations as journaled runs.
// A rotation is committed by the substrate (the new keypair lands in the
// credential Secret and the claim worker converges the store, the mirror,
// and the version bump from it); the controller here owns the run that
// explains it: it issues the commit, waits for the substrate to take it,
// and waits for the consuming applications to roll onto the new keys. The
// previous keypair retires on the substrate's own clock, run or no run.
package rotation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"k8s.io/client-go/util/workqueue"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/deploy"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/reconcile"
	"github.com/Hinkolas/skali/internal/revision"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate"
)

// Kind is the journal run kind of a credential rotation.
const Kind = "rotation"

var (
	ErrEnvironmentNotFound = errors.New("rotation: environment not found")
	// ErrBucketNotFound: the environment has no live claim for the service.
	ErrBucketNotFound = errors.New("rotation: no live bucket claim for the service")
	// ErrRunInFlight: the environment already has a running run (a
	// deployment, a backup, another rotation); the journal's
	// one-running-run index is the arbiter.
	ErrRunInFlight = errors.New("rotation: another run is in flight for this environment")

	errCancelled = errors.New("rotation: cancelled")
)

// BucketRotator is the substrate's commit; *substrate.Controller implements it.
type BucketRotator interface {
	RotateBucketCredentials(ctx context.Context, environmentID uuid.UUID, serviceKey string, retireAfter time.Duration) (substrate.BucketRotation, error)
}

// revisionLoader is the slice of deploy.Service the controller reads.
type revisionLoader interface {
	GetRevision(ctx context.Context, id uuid.UUID) (*revision.Revision, error)
}

type Deps struct {
	Store   *store.Store
	Journal *journal.Service
	DB      *dbstore.Service
	// Revisions loads the target revision to name the consumers; the
	// deploy service in production.
	Revisions revisionLoader
	Buckets   BucketRotator
	// Status projects one environment's health; the reconcile kernel's
	// Status in production.
	Status func(ctx context.Context, environmentID uuid.UUID) (*reconcile.Status, error)
}

type Config struct {
	Workers int
	// CommitTimeout bounds how long the run waits for the claim worker to
	// take the committed keypair (store, mirror, version bump).
	CommitTimeout time.Duration
	// RollTimeout bounds how long the run waits for the consumers to roll
	// onto the new keys; on timeout the run fails but the rotation stands.
	RollTimeout time.Duration
	// PollInterval paces both waits.
	PollInterval time.Duration
}

// Input names one rotation and who asked for it.
type Input struct {
	EnvironmentID uuid.UUID
	ServiceKey    string
	RetireAfter   time.Duration
	Actor         string
}

// Controller executes rotation runs. Create with New, start with Run;
// Create is the API-facing entry point.
type Controller struct {
	deps  Deps
	cfg   Config
	queue workqueue.TypedRateLimitingInterface[uuid.UUID]
	now   func() time.Time

	mu sync.Mutex
	// pending holds the input of every accepted run until its worker
	// takes it; a restart loses them, and RecoverOnBoot fails the runs.
	pending map[uuid.UUID]Input
}

func New(deps Deps, cfg Config) *Controller {
	if cfg.Workers <= 0 {
		cfg.Workers = 1
	}
	if cfg.CommitTimeout <= 0 {
		cfg.CommitTimeout = 2 * time.Minute
	}
	if cfg.RollTimeout <= 0 {
		cfg.RollTimeout = 10 * time.Minute
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 3 * time.Second
	}
	return &Controller{
		deps: deps,
		cfg:  cfg,
		queue: workqueue.NewTypedRateLimitingQueue(
			workqueue.DefaultTypedControllerRateLimiter[uuid.UUID]()),
		now:     time.Now,
		pending: map[uuid.UUID]Input{},
	}
}

// Create accepts a bucket credential rotation: the claim must be live,
// provisioned, and not fenced by a restore, and the environment must have
// no run in flight. The run is journaled and queued; the caller attaches
// to it.
func (c *Controller) Create(ctx context.Context, in Input) (uuid.UUID, error) {
	environment, err := c.deps.Store.GetEnvironmentByID(ctx, in.EnvironmentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, ErrEnvironmentNotFound
		}
		return uuid.Nil, fmt.Errorf("rotation: get environment: %w", err)
	}
	claimRow, err := c.deps.DB.LiveServiceBucketClaim(ctx, in.EnvironmentID, in.ServiceKey)
	if err != nil {
		if errors.Is(err, dbstore.ErrNotFound) {
			return uuid.Nil, ErrBucketNotFound
		}
		return uuid.Nil, err
	}
	if claim.Phase(claimRow.Phase) != claim.PhaseProvisioned {
		return uuid.Nil, substrate.ErrBucketNotProvisioned
	}
	allocation, err := c.deps.DB.LiveAllocation(ctx, claimRow.ID)
	if err != nil {
		if errors.Is(err, dbstore.ErrNotFound) {
			return uuid.Nil, substrate.ErrBucketNotProvisioned
		}
		return uuid.Nil, err
	}
	if allocation.FencedAt != nil {
		return uuid.Nil, substrate.ErrBucketFenced
	}

	run, err := c.deps.Journal.CreateRun(ctx, journal.RunInput{
		Kind:          Kind,
		ProjectID:     environment.ProjectID,
		EnvironmentID: in.EnvironmentID,
		Actor:         in.Actor,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("rotation: create run: %w", err)
	}
	if err := c.deps.Journal.StartRun(ctx, run.ID); err != nil {
		_ = c.deps.Journal.DiscardRun(ctx, run.ID)
		if errors.Is(err, journal.ErrRunConflict) {
			return uuid.Nil, ErrRunInFlight
		}
		return uuid.Nil, fmt.Errorf("rotation: start run: %w", err)
	}
	c.mu.Lock()
	c.pending[run.ID] = in
	c.mu.Unlock()
	c.queue.Add(run.ID)
	return run.ID, nil
}

// RecoverOnBoot fails every rotation run the previous process left
// running: its input died with that process, and a run left running
// would hold the environment's one-running-run slot forever. The rotation
// itself is unaffected, the substrate converges it from the Secret.
func (c *Controller) RecoverOnBoot(ctx context.Context) error {
	runs, err := c.deps.Store.ListRunningRunsByKind(ctx, Kind)
	if err != nil {
		return fmt.Errorf("rotation: list running runs: %w", err)
	}
	for _, run := range runs {
		message := "the daemon restarted during the rotation; the substrate completes it on its own " +
			"and the previous key retires at its scheduled time"
		if err := c.deps.Journal.FailRun(ctx, run.ID, nil, message); err != nil &&
			!errors.Is(err, journal.ErrInvalidTransition) && !errors.Is(err, journal.ErrNotFound) {
			slog.WarnContext(ctx, "rotation: finish interrupted run", "run", run.ID, "err", err)
		}
	}
	return nil
}

// Run processes rotation work until the context ends.
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
	if err := c.process(ctx, id); err != nil {
		slog.ErrorContext(ctx, "rotation: process", "run", id, "err", err)
	}
	c.queue.Forget(id)
	return true
}

// process executes one run end to end and drives it terminal. Nothing a
// rotation logs carries a secret, so the scope needs no redactor.
func (c *Controller) process(ctx context.Context, runID uuid.UUID) error {
	c.mu.Lock()
	in, ok := c.pending[runID]
	delete(c.pending, runID)
	c.mu.Unlock()
	if !ok {
		return nil
	}
	scope := journal.NewScope(c.deps.Journal, nil, runID)
	if err := c.execute(ctx, scope, in); err != nil {
		status, message := journal.RunFailed, err.Error()
		if errors.Is(err, errCancelled) {
			status, message = journal.RunCancelled, "cancelled"
		}
		scope.Finish(ctx, status, message)
		return nil
	}
	scope.Finish(ctx, journal.RunSucceeded, "")
	return nil
}

func (c *Controller) execute(ctx context.Context, scope *journal.Scope, in Input) error {
	var result substrate.BucketRotation
	var bumpedAt time.Time
	if err := scope.Step(ctx, "rotate", "Issue a new keypair", func(ctx context.Context, log *journal.StepLog) error {
		var err error
		result, err = c.deps.Buckets.RotateBucketCredentials(ctx, in.EnvironmentID, in.ServiceKey, in.RetireAfter)
		if err != nil {
			return err
		}
		log.Info(ctx, fmt.Sprintf("new keypair committed for buckets.%s; the previous key retires at %s",
			in.ServiceKey, result.RetireAt.Format(time.RFC3339)))
		version, err := c.awaitCommit(ctx, scope, in, result.AccessKey)
		if err != nil {
			return err
		}
		bumpedAt = c.now()
		log.Info(ctx, fmt.Sprintf("the store accepts both keys and the environment mirrors the new one; credentials v%d", version))
		return nil
	}); err != nil {
		return err
	}

	status, err := c.deps.Status(ctx, in.EnvironmentID)
	if err != nil {
		return fmt.Errorf("read environment status: %w", err)
	}
	if status.State != deploy.EnvironmentStateActive || status.Target == nil {
		scope.Skip(ctx, "roll", "Roll the consuming applications (the environment is not active)")
		return nil
	}
	rev, err := c.deps.Revisions.GetRevision(ctx, status.Target.ID)
	if err != nil {
		return fmt.Errorf("load target revision: %w", err)
	}
	consumers := compiler.ServiceConsumers(&rev.Definition, "buckets", in.ServiceKey)
	if len(consumers) == 0 {
		scope.Skip(ctx, "roll", fmt.Sprintf("Roll the consuming applications (no application references buckets.%s)", in.ServiceKey))
		return nil
	}
	return scope.Step(ctx, "roll", "Roll the consuming applications", func(ctx context.Context, log *journal.StepLog) error {
		return c.awaitRoll(ctx, scope, log, in, consumers, bumpedAt, result.RetireAt)
	})
}

// awaitCommit polls the allocation until the claim worker has taken the
// committed key: by then the store accepts both keys, the mirror holds the
// new pair, and the version bump is visible to the kernel.
func (c *Controller) awaitCommit(ctx context.Context, scope *journal.Scope, in Input, accessKey string) (int64, error) {
	deadline := c.now().Add(c.cfg.CommitTimeout)
	for {
		claimRow, err := c.deps.DB.LiveServiceBucketClaim(ctx, in.EnvironmentID, in.ServiceKey)
		if err != nil {
			return 0, fmt.Errorf("the bucket claim went away: %w", err)
		}
		allocation, err := c.deps.DB.LiveAllocation(ctx, claimRow.ID)
		if err != nil {
			return 0, fmt.Errorf("the bucket allocation went away: %w", err)
		}
		if allocation.AccessKeyID == accessKey {
			return allocation.CredentialVersion, nil
		}
		if scope.Cancelled(ctx) {
			return 0, errCancelled
		}
		if !c.now().Before(deadline) {
			return 0, errors.New("the substrate did not take the new keypair in time; it will on its next pass, " +
				"and the previous key still retires at its scheduled time")
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(c.cfg.PollInterval):
		}
	}
}

// awaitRoll polls the environment projection until every consumer runs
// only pods started after the bump, healthy. On timeout the run fails but
// the rotation stands: reconciliation keeps rolling the consumers and the
// previous key retires on schedule.
func (c *Controller) awaitRoll(ctx context.Context, scope *journal.Scope, log *journal.StepLog, in Input,
	consumers []string, since, retireAt time.Time) error {
	log.Info(ctx, "waiting for "+strings.Join(consumers, ", ")+" to restart with the new keypair")
	deadline := c.now().Add(c.cfg.RollTimeout)
	lastPending := ""
	for {
		status, err := c.deps.Status(ctx, in.EnvironmentID)
		if err != nil {
			return fmt.Errorf("read environment status: %w", err)
		}
		if status.State != deploy.EnvironmentStateActive {
			return fmt.Errorf("the environment is %s; the consumers roll when it is deployed again", status.State)
		}
		pending := pendingConsumers(status, consumers, since)
		if len(pending) == 0 {
			log.Info(ctx, "every consumer runs with the new keypair")
			return nil
		}
		if joined := strings.Join(pending, ", "); joined != lastPending {
			log.Info(ctx, "still rolling: "+joined)
			lastPending = joined
		}
		if scope.Cancelled(ctx) {
			return errCancelled
		}
		if !c.now().Before(deadline) {
			return fmt.Errorf("%s did not roll in time; reconciliation continues and the previous key still retires at %s",
				strings.Join(pending, ", "), retireAt.Format(time.RFC3339))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.cfg.PollInterval):
		}
	}
}

// pendingConsumers names the consumers that still run a pod started
// before since, are not healthy, or are not projected yet. Every pod
// counts, serving or not: a blue-green drain's old-color pods still hold
// the previous key. An intercepted application (served by a host process)
// and one scaled to zero replicas hold no pod to roll. Health alone would
// not do: a rolling application reads healthy between the version bump
// and the informer delivering the new generation.
func pendingConsumers(status *reconcile.Status, consumers []string, since time.Time) []string {
	since = since.Truncate(time.Second) // pod start times carry seconds
	services := make(map[string]reconcile.ServiceStatus, len(status.Services))
	for _, service := range status.Services {
		services[service.Key] = service
	}
	var pending []string
	for _, key := range consumers {
		service, ok := services[key]
		if !ok || status.Observation.State != module.SourceFresh {
			pending = append(pending, key)
			continue
		}
		if service.Intercepted {
			continue
		}
		if len(service.Pods) == 0 {
			if !hasDiagnostic(service, "no-replicas") {
				pending = append(pending, key)
			}
			continue
		}
		rolled := service.Health == module.HealthHealthy
		for _, pod := range service.Pods {
			if pod.Started.Before(since) {
				rolled = false
			}
		}
		if !rolled {
			pending = append(pending, key)
		}
	}
	sort.Strings(pending)
	return pending
}

func hasDiagnostic(service reconcile.ServiceStatus, code string) bool {
	for _, diagnostic := range service.Diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}
