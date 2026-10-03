// Package rotation drives managed credential rotations as journaled runs.
// A rotation is committed by the substrate (a bucket's new keypair lands
// in the credential Secret, a database's next login role in the tenant
// row, and the claim worker converges the store or pool, the mirror, and
// the version bump from it); the controller here owns the run that
// explains it: it issues the commit, waits for the substrate to take it,
// and waits for the consuming applications to roll onto the new
// credentials. The previous ones retire on the substrate's own clock, run
// or no run.
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

// Collections a rotation applies to: the service collections of the
// definition.
const (
	CollectionBuckets   = "buckets"
	CollectionDatabases = "databases"
)

var (
	ErrEnvironmentNotFound = errors.New("rotation: environment not found")
	// ErrBucketNotFound: the environment has no live bucket claim for the service.
	ErrBucketNotFound = errors.New("rotation: no live bucket claim for the service")
	// ErrDatabaseNotFound: the environment has no live database claim for the service.
	ErrDatabaseNotFound = errors.New("rotation: no live database claim for the service")
	// ErrRunInFlight: the environment already has a running run (a
	// deployment, a backup, another rotation); the journal's
	// one-running-run index is the arbiter.
	ErrRunInFlight = errors.New("rotation: another run is in flight for this environment")

	errCancelled = errors.New("rotation: cancelled")
)

// BucketRotator is the substrate's bucket commit; *substrate.Controller implements it.
type BucketRotator interface {
	RotateBucketCredentials(ctx context.Context, environmentID uuid.UUID, serviceKey string, retireAfter time.Duration) (substrate.BucketRotation, error)
}

// DatabaseRotator is the substrate's database commit and the early
// retirement a second rotation inside a window needs; *substrate.Controller
// implements it.
type DatabaseRotator interface {
	RotateDatabaseCredentials(ctx context.Context, environmentID uuid.UUID, serviceKey string, retireAfter time.Duration) (substrate.DatabaseRotation, error)
	RetireDatabaseCredentials(ctx context.Context, environmentID uuid.UUID, serviceKey string) error
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
	Databases DatabaseRotator
	// Status projects one environment's health; the reconcile kernel's
	// Status in production.
	Status func(ctx context.Context, environmentID uuid.UUID) (*reconcile.Status, error)
}

type Config struct {
	Workers int
	// CommitTimeout bounds how long the run waits for the claim worker to
	// take the committed credentials (store or pool, mirror, version bump),
	// and for an early retirement a second rotation inside a window needs.
	CommitTimeout time.Duration
	// RollTimeout bounds how long the run waits for the consumers to roll
	// onto the new credentials; on timeout the run fails but the rotation
	// stands.
	RollTimeout time.Duration
	// PollInterval paces the waits.
	PollInterval time.Duration
}

// Input names one rotation and who asked for it.
type Input struct {
	// Collection is CollectionBuckets or CollectionDatabases.
	Collection    string
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

// Create accepts a credential rotation: the claim must be live and
// provisioned (a bucket also not fenced by a restore), and the environment
// must have no run in flight. The run is journaled and queued; the caller
// attaches to it.
func (c *Controller) Create(ctx context.Context, in Input) (uuid.UUID, error) {
	environment, err := c.deps.Store.GetEnvironmentByID(ctx, in.EnvironmentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, ErrEnvironmentNotFound
		}
		return uuid.Nil, fmt.Errorf("rotation: get environment: %w", err)
	}
	switch in.Collection {
	case CollectionBuckets:
		if err := c.checkBucket(ctx, in); err != nil {
			return uuid.Nil, err
		}
	case CollectionDatabases:
		if err := c.checkDatabase(ctx, in); err != nil {
			return uuid.Nil, err
		}
	default:
		return uuid.Nil, fmt.Errorf("rotation: unknown collection %q", in.Collection)
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

func (c *Controller) checkBucket(ctx context.Context, in Input) error {
	if c.deps.Buckets == nil {
		return errors.New("rotation: bucket rotation is not available")
	}
	claimRow, err := c.deps.DB.LiveServiceBucketClaim(ctx, in.EnvironmentID, in.ServiceKey)
	if err != nil {
		if errors.Is(err, dbstore.ErrNotFound) {
			return ErrBucketNotFound
		}
		return err
	}
	if claim.Phase(claimRow.Phase) != claim.PhaseProvisioned {
		return substrate.ErrBucketNotProvisioned
	}
	allocation, err := c.deps.DB.LiveAllocation(ctx, claimRow.ID)
	if err != nil {
		if errors.Is(err, dbstore.ErrNotFound) {
			return substrate.ErrBucketNotProvisioned
		}
		return err
	}
	if allocation.FencedAt != nil {
		return substrate.ErrBucketFenced
	}
	return nil
}

func (c *Controller) checkDatabase(ctx context.Context, in Input) error {
	if c.deps.Databases == nil {
		return errors.New("rotation: database rotation is not available")
	}
	claimRow, err := c.deps.DB.LiveServiceClaim(ctx, in.EnvironmentID, in.ServiceKey)
	if err != nil {
		if errors.Is(err, dbstore.ErrNotFound) {
			return ErrDatabaseNotFound
		}
		return err
	}
	if claim.Phase(claimRow.Phase) != claim.PhaseProvisioned {
		return substrate.ErrDatabaseNotProvisioned
	}
	if _, err := c.deps.DB.LiveTenant(ctx, claimRow.ID); err != nil {
		if errors.Is(err, dbstore.ErrNotFound) {
			return substrate.ErrDatabaseNotProvisioned
		}
		return err
	}
	return nil
}

// RecoverOnBoot fails every rotation run the previous process left
// running: its input died with that process, and a run left running
// would hold the environment's one-running-run slot forever. The rotation
// itself is unaffected, the substrate converges it from its own state.
func (c *Controller) RecoverOnBoot(ctx context.Context) error {
	runs, err := c.deps.Store.ListRunningRunsByKind(ctx, Kind)
	if err != nil {
		return fmt.Errorf("rotation: list running runs: %w", err)
	}
	for _, run := range runs {
		message := "the daemon restarted during the rotation; the substrate completes it on its own " +
			"and the previous credentials retire at their scheduled time"
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

// words is the vocabulary one collection's run speaks in.
type words struct {
	credential string // "keypair", "login role"
	previous   string // "the previous key", "the previous login role"
	engine     string // "the store accepts both keys", "the pool holds both roles"
}

func vocabulary(collection string) words {
	if collection == CollectionDatabases {
		return words{credential: "login role", previous: "the previous login role", engine: "the pool holds both roles"}
	}
	return words{credential: "keypair", previous: "the previous key", engine: "the store accepts both keys"}
}

func (c *Controller) execute(ctx context.Context, scope *journal.Scope, in Input) error {
	w := vocabulary(in.Collection)
	var retireAt time.Time
	var bumpedAt time.Time
	if err := scope.Step(ctx, "rotate", "Issue a new "+w.credential, func(ctx context.Context, log *journal.StepLog) error {
		var version int64
		var err error
		switch in.Collection {
		case CollectionDatabases:
			retireAt, version, err = c.rotateDatabase(ctx, scope, log, in)
		default:
			retireAt, version, err = c.rotateBucket(ctx, scope, log, in)
		}
		if err != nil {
			return err
		}
		bumpedAt = c.now()
		log.Info(ctx, fmt.Sprintf("%s and the environment mirrors the new one; credentials v%d", w.engine, version))
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
	consumers := compiler.ServiceConsumers(&rev.Definition, in.Collection, in.ServiceKey)
	if len(consumers) == 0 {
		scope.Skip(ctx, "roll", fmt.Sprintf("Roll the consuming applications (no application references %s.%s)", in.Collection, in.ServiceKey))
		return nil
	}
	return scope.Step(ctx, "roll", "Roll the consuming applications", func(ctx context.Context, log *journal.StepLog) error {
		return c.awaitRoll(ctx, scope, log, in, consumers, bumpedAt, retireAt)
	})
}

// rotateBucket commits a bucket rotation and waits for the worker to take
// it; it returns the retirement instant and the new credential version.
func (c *Controller) rotateBucket(ctx context.Context, scope *journal.Scope, log *journal.StepLog, in Input) (time.Time, int64, error) {
	result, err := c.deps.Buckets.RotateBucketCredentials(ctx, in.EnvironmentID, in.ServiceKey, in.RetireAfter)
	if err != nil {
		return time.Time{}, 0, err
	}
	log.Info(ctx, fmt.Sprintf("new keypair committed for buckets.%s; the previous key retires at %s",
		in.ServiceKey, result.RetireAt.Format(time.RFC3339)))
	version, err := c.await(ctx, scope, "the substrate did not take the new keypair in time; it will on its next pass, "+
		"and the previous key still retires at its scheduled time", func(ctx context.Context) (int64, bool, error) {
		claimRow, err := c.deps.DB.LiveServiceBucketClaim(ctx, in.EnvironmentID, in.ServiceKey)
		if err != nil {
			return 0, false, fmt.Errorf("the bucket claim went away: %w", err)
		}
		allocation, err := c.deps.DB.LiveAllocation(ctx, claimRow.ID)
		if err != nil {
			return 0, false, fmt.Errorf("the bucket allocation went away: %w", err)
		}
		return allocation.CredentialVersion, allocation.AccessKeyID == result.AccessKey, nil
	})
	return result.RetireAt, version, err
}

// rotateDatabase commits a database rotation and waits for the worker to
// take it. A previous login role still inside an earlier rotation's window
// is retired first (rotating again inside the window retires the older
// credentials at once, as for buckets), since a tenant carries one
// previous role at a time.
func (c *Controller) rotateDatabase(ctx context.Context, scope *journal.Scope, log *journal.StepLog, in Input) (time.Time, int64, error) {
	tenant, err := c.liveTenant(ctx, in)
	if err != nil {
		return time.Time{}, 0, err
	}
	if tenant.PreviousLoginRole != nil && tenant.PendingLoginRole == nil {
		log.Info(ctx, fmt.Sprintf("retiring %s from the previous rotation first", *tenant.PreviousLoginRole))
		if err := c.deps.Databases.RetireDatabaseCredentials(ctx, in.EnvironmentID, in.ServiceKey); err != nil {
			return time.Time{}, 0, err
		}
		if _, err := c.await(ctx, scope, "the substrate did not retire the previous login role in time; "+
			"it will on its next pass, after which the rotation can be run again", func(ctx context.Context) (int64, bool, error) {
			tenant, err := c.liveTenant(ctx, in)
			if err != nil {
				return 0, false, err
			}
			return tenant.CredentialVersion, tenant.PreviousLoginRole == nil, nil
		}); err != nil {
			return time.Time{}, 0, err
		}
	}
	result, err := c.deps.Databases.RotateDatabaseCredentials(ctx, in.EnvironmentID, in.ServiceKey, in.RetireAfter)
	if err != nil {
		return time.Time{}, 0, err
	}
	log.Info(ctx, fmt.Sprintf("new login role %s committed for databases.%s; the previous login role retires at %s",
		result.LoginRole, in.ServiceKey, result.RetireAt.Format(time.RFC3339)))
	version, err := c.await(ctx, scope, "the substrate did not take the new login role in time; it will on its next pass, "+
		"and the previous login role still retires at its scheduled time", func(ctx context.Context) (int64, bool, error) {
		tenant, err := c.liveTenant(ctx, in)
		if err != nil {
			return 0, false, err
		}
		return tenant.CredentialVersion, tenant.LoginRole == result.LoginRole, nil
	})
	return result.RetireAt, version, err
}

func (c *Controller) liveTenant(ctx context.Context, in Input) (*store.DatabaseTenant, error) {
	claimRow, err := c.deps.DB.LiveServiceClaim(ctx, in.EnvironmentID, in.ServiceKey)
	if err != nil {
		return nil, fmt.Errorf("the database claim went away: %w", err)
	}
	tenant, err := c.deps.DB.LiveTenant(ctx, claimRow.ID)
	if err != nil {
		return nil, fmt.Errorf("the database tenant went away: %w", err)
	}
	return tenant, nil
}

// await polls check until it reports done, bounded by CommitTimeout; by
// then the claim worker has taken the committed credentials (store or
// pool, mirror, version bump) and the version is visible to the kernel.
func (c *Controller) await(ctx context.Context, scope *journal.Scope, timeoutMessage string,
	check func(ctx context.Context) (int64, bool, error)) (int64, error) {
	deadline := c.now().Add(c.cfg.CommitTimeout)
	for {
		version, done, err := check(ctx)
		if err != nil {
			return 0, err
		}
		if done {
			return version, nil
		}
		if scope.Cancelled(ctx) {
			return 0, errCancelled
		}
		if !c.now().Before(deadline) {
			return 0, errors.New(timeoutMessage)
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
// previous credentials retire on schedule.
func (c *Controller) awaitRoll(ctx context.Context, scope *journal.Scope, log *journal.StepLog, in Input,
	consumers []string, since, retireAt time.Time) error {
	w := vocabulary(in.Collection)
	log.Info(ctx, "waiting for "+strings.Join(consumers, ", ")+" to restart with the new "+w.credential)
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
			log.Info(ctx, "every consumer runs with the new "+w.credential)
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
			return fmt.Errorf("%s did not roll in time; reconciliation continues and %s still retires at %s",
				strings.Join(pending, ", "), w.previous, retireAt.Format(time.RFC3339))
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
// the previous credentials. An intercepted application (served by a host
// process) and one scaled to zero replicas hold no pod to roll. Health
// alone would not do: a rolling application reads healthy between the
// version bump and the informer delivering the new generation.
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
