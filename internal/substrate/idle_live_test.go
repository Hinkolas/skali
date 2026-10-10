package substrate

import (
	"context"
	"fmt"
	"maps"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/kubetest"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
	"github.com/Hinkolas/skali/internal/testdb"
	"github.com/Hinkolas/skali/internal/workstats"
)

// enqueueCounter counts the environment passes the storage probe asks the
// kernel for.
type enqueueCounter struct {
	mu     sync.Mutex
	counts map[uuid.UUID]int
}

func (c *enqueueCounter) add(environmentID uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[environmentID]++
}

func (c *enqueueCounter) snapshot() map[uuid.UUID]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return maps.Clone(c.counts)
}

// arrivalsBut sums a queue's arrivals over every reason but the ones
// given, per kind.
func arrivalsBut(stats workstats.QueueStats, skip ...string) map[string]uint64 {
	sums := map[string]uint64{}
	for _, kind := range stats.Kinds {
		for reason, count := range kind.Arrivals {
			skipped := false
			for _, s := range skip {
				skipped = skipped || reason == s
			}
			if !skipped {
				sums[kind.Kind] += count
			}
		}
	}
	return sums
}

// TestLiveIdleBucketsCauseNoPasses guards what keeps the queues near zero
// on an idle installation: with several bucket environments provisioned
// and the substrate running as in production, the storage probe polls an
// unchanged store over and over, and no poll asks for an environment pass
// or queues substrate work. Only the claims' scheduled repairs arrive. A
// real change still reaches the kernel, for the one environment it
// touches. Requires TEST_KUBECONFIG and TEST_DATABASE_URL.
func TestLiveIdleBucketsCauseNoPasses(t *testing.T) {
	defer livePhase(t, "scenario")()
	config := kubetest.Config(t)
	pool := testdb.New(t)
	ctx := context.Background()

	client, err := kube.NewFromConfig(config)
	require.NoError(t, err)
	cleanupPlatform(t, client)
	installOperator(t, client)

	st := store.NewStore(pool)
	dbSvc := dbstore.New(st)
	projects := project.New(st)
	observed := observe.NewStore(nil)
	controller := New(Deps{
		DB:       dbSvc,
		Cluster:  KubeCluster{Client: client},
		Observed: observed,
		Seaweed:  seaweed.NewClient(client, Namespace),
	}, Config{Managed: false})

	const environments = 3
	suffix := uuid.Must(uuid.NewV7()).String()[24:]
	envs := make([]*store.Environment, environments)
	buckets := make([]string, environments)
	for i := range environments {
		proj, err := projects.Create(ctx, fmt.Sprintf("idle%d%s", i, suffix), "", uuid.Nil)
		require.NoError(t, err)
		env, err := projects.CreateEnvironment(ctx, proj.ID, "production", project.EnvironmentOptions{})
		require.NoError(t, err)
		namespace := kubernetes.RenderNamespace(proj.Name, "production", env.ID.String())
		t.Cleanup(func() { deleteNamespace(t, client, namespace.Name) })
		_, err = client.Apply(ctx, namespace, false)
		require.NoError(t, err)
		envs[i] = env
		buckets[i] = driveLiveBucket(t, controller, dbSvc, proj, env, "files").BucketName
	}

	// The substrate's workers and storage upkeep run as in production, and
	// the storage probe at a tight cadence reports to the counter what the
	// kernel would be asked to pass.
	enqueued := &enqueueCounter{counts: map[uuid.UUID]int{}}
	poll := observe.NewPollSource(observed, controller.SeaweedProbe(), observe.PollOptions{
		Source:         seaweed.SourceName,
		Interval:       2 * time.Second,
		StaleThreshold: 8 * time.Second,
		Enqueue:        enqueued.add,
	})
	controller.SetProbePoke(poll.Poke)
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	go controller.Run(runCtx)
	go func() { _ = poll.Run(runCtx) }()

	// Settled: the source is fresh, every bucket is observed, the start's
	// work and the first storage upkeep pass are done, and the queue is
	// empty.
	defer livePhase(t, "idle window")()
	requireEventually(t, time.Minute, func() bool {
		source, ok := observed.SourceNamed(seaweed.SourceName)
		if !ok || source.State != module.SourceFresh {
			return false
		}
		for _, env := range envs {
			found := false
			for _, resource := range observed.Snapshot(env.ID).ForService("buckets.files") {
				found = found || (resource.Kind == module.KindBucket && resource.Bucket.Exists)
			}
			if !found {
				return false
			}
		}
		return true
	}, "the store and every bucket were never observed")
	time.Sleep(20 * time.Second) // past the first storage upkeep pass
	requireEventually(t, time.Minute, func() bool {
		stats := controller.QueueStats()
		return stats.Depth == 0 && stats.Active == 0
	}, "the substrate queue never emptied")

	// Fifteen polls of the idle store: no environment pass, no substrate
	// work but the scheduled repairs.
	enqueuedBefore := enqueued.snapshot()
	arrivedBefore := arrivalsBut(controller.QueueStats(), reasonRepair)
	time.Sleep(30 * time.Second)
	require.Equal(t, enqueuedBefore, enqueued.snapshot(), "an identical poll asked for an environment pass")
	require.Equal(t, arrivedBefore, arrivalsBut(controller.QueueStats(), reasonRepair),
		"an idle store queued substrate work")

	// A real change: one bucket disappears from the store. The next poll
	// asks for its environment's pass, and for no other.
	require.NoError(t, controller.deps.Seaweed.DeleteBucket(ctx, buckets[0]))
	requireEventually(t, 30*time.Second, func() bool {
		return enqueued.snapshot()[envs[0].ID] > enqueuedBefore[envs[0].ID]
	}, "the poll that saw the bucket gone did not ask for its environment's pass")
	after := enqueued.snapshot()
	for _, env := range envs[1:] {
		require.Equal(t, enqueuedBefore[env.ID], after[env.ID], "an untouched environment was enqueued")
	}
}
