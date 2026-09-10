package reconcile

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/workqueue"

	"github.com/Hinkolas/skali/internal/artifactstore"
	"github.com/Hinkolas/skali/internal/deploy"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/kube"
	rendering "github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/module/app"
	"github.com/Hinkolas/skali/internal/module/bucket"
	"github.com/Hinkolas/skali/internal/module/database"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/testdb"
	"github.com/Hinkolas/skali/internal/valuestore"
)

const kernelManifest = `version: "1"
name: demo
applications:
  web:
    image: ghcr.io/example/web:1.0.0
    ports:
      http:
        port: 8080
        protocol: http
    environment:
      APP_DOMAIN: "${APP_DOMAIN}"
      SESSION_SECRET: "${SESSION_SECRET}"
`

// fakeCluster records planned operations; the first apply of an object is
// material, repeats are no-ops, mirroring server-side-apply idempotence.
type fakeCluster struct {
	mu      sync.Mutex
	ops     []string
	applied map[string]bool
	// objects keeps the last applied object per key so tests can inspect
	// what was sent (a Service's selector, a Deployment's replicas).
	objects map[string]runtime.Object
}

func newFakeCluster() *fakeCluster {
	return &fakeCluster{applied: make(map[string]bool), objects: make(map[string]runtime.Object)}
}

// lastApplied returns the most recently applied object under
// "Kind/namespace/name", nil when none was applied.
func (f *fakeCluster) lastApplied(key string) runtime.Object {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.objects[key]
}

func (f *fakeCluster) Apply(_ context.Context, obj runtime.Object, force bool) (kube.ApplyResult, error) {
	accessor, err := meta.Accessor(obj)
	if err != nil {
		return kube.ApplyResult{}, err
	}
	key := obj.GetObjectKind().GroupVersionKind().Kind + "/" + accessor.GetNamespace() + "/" + accessor.GetName()
	f.mu.Lock()
	defer f.mu.Unlock()
	changed := !f.applied[key]
	f.applied[key] = true
	f.objects[key] = obj
	entry := "apply " + key
	if force {
		entry += " (forced)"
	}
	f.ops = append(f.ops, entry)
	return kube.ApplyResult{Changed: changed}, nil
}

func (f *fakeCluster) Delete(_ context.Context, ref kube.ObjectRef) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops = append(f.ops, "delete "+ref.String())
	return true, nil
}

func (f *fakeCluster) DisownFields(_ context.Context, ref kube.ObjectRef, manager string, paths ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops = append(f.ops, "disown "+strings.Join(paths, ",")+" "+ref.String())
	return nil
}

func (f *fakeCluster) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ops...)
}

type kernelFixture struct {
	st      *store.Store
	kernel  *Kernel
	journal *journal.Service
	deploy  *deploy.Service
	fake    *observe.Fake
	cluster *fakeCluster

	projectID     uuid.UUID
	environmentID uuid.UUID
	revisionID    uuid.UUID
	namespace     string
}

func newKernelFixture(t *testing.T, cfg Config) *kernelFixture {
	t.Helper()
	ctx := context.Background()
	pool := testdb.New(t)
	st := store.NewStore(pool)
	projects := project.New(st)
	valueSvc, err := valuestore.New(st, strings.Repeat("k", 32))
	require.NoError(t, err)
	artifactSvc := artifactstore.New(st)
	deploySvc := deploy.New(st, valueSvc, artifactSvc, "test")
	journalSvc := journal.NewService(st, "kernel-test-boot-1")
	registry := module.NewRegistry()
	require.NoError(t, registry.Register(app.Module{}))
	require.NoError(t, registry.Register(database.Module{}))
	require.NoError(t, registry.Register(bucket.Module{}))
	fake := observe.NewFake()
	cluster := newFakeCluster()

	kernel := New(Deps{
		Store:    st,
		Deploy:   deploySvc,
		Values:   valueSvc,
		Journal:  journalSvc,
		Registry: registry,
		Observed: fake.Store,
		Cluster:  cluster,
		HostGateway: func(context.Context) (string, error) {
			return "192.0.2.10", nil
		},
	}, cfg)
	deploySvc.SetEnqueuer(kernel)

	proj, err := projects.Create(ctx, "demo", "", uuid.Nil)
	require.NoError(t, err)
	env, err := projects.CreateEnvironment(ctx, proj.ID, "production", project.EnvironmentOptions{})
	require.NoError(t, err)

	return &kernelFixture{
		st: st, kernel: kernel, journal: journalSvc, deploy: deploySvc,
		fake: fake, cluster: cluster,
		projectID: proj.ID, environmentID: env.ID,
		namespace: "skali-" + env.ID.String(),
	}
}

// executeDeployment promotes the manifest and leaves the deployment run
// running with a pending rollout step (the kernel is wired as enqueuer).
func (f *kernelFixture) executeDeployment(t *testing.T) *deploy.ExecuteResult {
	t.Helper()
	return f.executeDeploymentManifest(t, kernelManifest)
}

func (f *kernelFixture) executeDeploymentManifest(t *testing.T, manifestSource string) *deploy.ExecuteResult {
	t.Helper()
	ctx := context.Background()
	projects := project.New(f.st)
	// Resubmitting on top of an earlier draft needs its version; a project
	// without a draft yet starts at zero.
	submission := project.DraftSubmission{Source: []byte(manifestSource), Format: "yaml"}
	if current, err := projects.GetDraft(ctx, f.projectID); err == nil {
		submission.ExpectedVersion = current.Version
	}
	draft, err := projects.SubmitDraft(ctx, f.projectID, submission)
	require.NoError(t, err)
	row, err := f.st.GetDefinitionVersionByHash(ctx, store.GetDefinitionVersionByHashParams{
		ProjectID: f.projectID, DefinitionHash: draft.Hash,
	})
	require.NoError(t, err)
	valueSvc, err := valuestore.New(f.st, strings.Repeat("k", 32))
	require.NoError(t, err)
	candidate, err := valueSvc.Stage(ctx, f.environmentID, map[string]string{
		"APP_DOMAIN":     "demo.example.com",
		"SESSION_SECRET": "kernel-plant-value",
	})
	require.NoError(t, err)

	result, err := f.deploy.Execute(ctx, deploy.ExecuteInput{
		ProjectID:           f.projectID,
		EnvironmentID:       f.environmentID,
		DefinitionVersionID: row.ID,
		CandidateID:         candidate.ID,
		Resolver:            &artifactstore.Fake{Store: artifactstore.New(f.st), ProjectID: f.projectID},
		Journal:             f.journal,
		Actor:               "tester",
	})
	require.NoError(t, err)
	f.revisionID = result.RevisionID
	return result
}

func (f *kernelFixture) target(t *testing.T) store.EnvironmentTarget {
	t.Helper()
	target, err := f.st.GetEnvironmentTarget(context.Background(), f.environmentID)
	require.NoError(t, err)
	return target
}

// webDeploymentName is the rendered name of the demo project's web
// Deployment for the current target revision. Blue-green workloads are
// named by their pod template color, so the fixture derives the name the
// way the kernel does instead of repeating a hash.
func (f *kernelFixture) webDeploymentName(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	target := f.target(t)
	require.NotNil(t, target.TargetRevisionID, "the fixture has no target revision to name a workload for")
	rev, err := f.deploy.GetRevision(ctx, *target.TargetRevisionID)
	require.NoError(t, err)
	intercepts, err := f.kernel.loadIntercepts(ctx, f.environmentID)
	require.NoError(t, err)
	colors, err := f.kernel.desiredColors(ctx, f.environmentID, target, rev, intercepts)
	require.NoError(t, err)
	if color, ok := colors["web"]; ok {
		return rendering.ColoredApplicationName("demo", "web", color)
	}
	return rendering.ApplicationName("demo", "web")
}

// webColor is the blue-green color the current target revision renders for
// the web application, empty when it renders an uncolored workload.
func (f *kernelFixture) webColor(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	target := f.target(t)
	require.NotNil(t, target.TargetRevisionID)
	rev, err := f.deploy.GetRevision(ctx, *target.TargetRevisionID)
	require.NoError(t, err)
	intercepts, err := f.kernel.loadIntercepts(ctx, f.environmentID)
	require.NoError(t, err)
	colors, err := f.kernel.desiredColors(ctx, f.environmentID, target, rev, intercepts)
	require.NoError(t, err)
	return colors["web"]
}

// setWebWorkload records the web application's Deployment for the current
// target revision under its rendered name and color, so the module's
// color-aware verdict sees the workload the kernel is rolling out.
func (f *kernelFixture) setWebWorkload(t *testing.T, revision string, status module.WorkloadStatus) {
	t.Helper()
	f.fake.SetColoredWorkload(f.environmentID, f.namespace, f.webDeploymentName(t), "web", revision, f.webColor(t), status)
}

// webServiceName is the rendered name of the demo project's web Service,
// stable across revisions by contract.
func (f *kernelFixture) webServiceName() string {
	return "app-demo-web-714832ea87e5bc991f3f11667354c6c3"
}

func (f *kernelFixture) markHealthy(t *testing.T) {
	t.Helper()
	target := f.target(t)
	require.NotNil(t, target.TargetRevisionID)
	row, err := f.st.GetRevisionByID(context.Background(), *target.TargetRevisionID)
	require.NoError(t, err)
	f.setWebWorkload(t, row.Checksum[:16],
		module.WorkloadStatus{Desired: 1, Ready: 1, Updated: 1})
	f.fake.SetPod(f.environmentID, f.namespace, "web", "web-1", "node-a",
		module.PodStatus{Phase: "Running", Ready: true})
}

// The full rollout: promote leaves the run running, the first pass applies
// and waits, health arrives, the second pass activates and finishes the
// SAME deployment run through deterministic step keys.
func TestReconcileAppliesAndActivatesDeployment(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	result := f.executeDeployment(t)
	f.fake.SetFresh()

	run, err := f.st.GetRunByID(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "running", run.Status, "the kernel, not Execute, concludes the run")

	requeue, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, requeueHealthCheck, requeue)

	ops := f.cluster.recorded()
	require.Contains(t, ops, "apply Namespace//"+f.namespace)
	require.Contains(t, ops, "apply Secret/"+f.namespace+"/skali-environment")
	require.Contains(t, ops, "apply Deployment/"+f.namespace+"/"+f.webDeploymentName(t))
	require.Contains(t, ops, "apply Service/"+f.namespace+"/"+f.webServiceName())

	target := f.target(t)
	require.Equal(t, result.RevisionID, *target.TargetRevisionID)
	require.Nil(t, target.ActiveRevisionID, "activation requires passing health")

	// Health arrives through observation; the next pass activates.
	f.markHealthy(t)
	requeue, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Zero(t, requeue)

	target = f.target(t)
	require.NotNil(t, target.ActiveRevisionID)
	require.Equal(t, *target.TargetRevisionID, *target.ActiveRevisionID)

	tree, err := f.journal.RunTree(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", tree.Run.Status)
	keys := map[string]string{}
	var collect func(steps []*journal.TreeStep)
	collect = func(steps []*journal.TreeStep) {
		for _, step := range steps {
			keys[step.Step.Key] = step.Step.Status
			collect(step.Children)
		}
	}
	collect(tree.Steps)
	require.Equal(t, "succeeded", keys["rollout"])
	require.Equal(t, "succeeded", keys["apply:web"])
	require.Equal(t, "succeeded", keys["verify"])
	require.Equal(t, "succeeded", keys["activate"])
}

// A pass that changes nothing writes no journal rows, and deleting every
// journal row changes no reconciliation behavior.
func TestReconcileIdlePassWritesNoJournal(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	f.executeDeployment(t)
	f.fake.SetFresh()
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	f.markHealthy(t)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	activeBefore := f.target(t)

	// Wipe the journal entirely: explanation, never authority.
	_, err = f.st.Pool.Exec(ctx, "DELETE FROM runs")
	require.NoError(t, err)

	requeue, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Zero(t, requeue)

	var runs int
	require.NoError(t, f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM runs").Scan(&runs))
	require.Zero(t, runs, "an idle pass must write no journal rows")

	after := f.target(t)
	require.Equal(t, activeBefore.TargetRevisionID, after.TargetRevisionID)
	require.Equal(t, activeBefore.ActiveRevisionID, after.ActiveRevisionID)
	require.Equal(t, activeBefore.UpdatedAt, after.UpdatedAt)
}

// Past the rollout deadline the run fails with diagnostics and the target
// stays; a later recovery still activates (level-triggered; the deadline
// fallback is a separate policy).
func TestReconcileDeadlineFailsRunKeepsTarget(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Nanosecond})
	ctx := context.Background()
	result := f.executeDeployment(t)
	f.fake.SetFresh()
	f.setWebWorkload(t, "",
		module.WorkloadStatus{Desired: 1, Ready: 0, Updated: 1})

	requeue, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, requeueHealthCheck, requeue,
		"a first deployment past its deadline keeps the reconcile cadence; late recovery must not wait for the audit")

	run, err := f.st.GetRunByID(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "failed", run.Status)

	target := f.target(t)
	require.Equal(t, result.RevisionID, *target.TargetRevisionID, "the target survives the failed run")
	require.Nil(t, target.ActiveRevisionID)

	// Late recovery: health arrives after the run failed; activation still
	// happens, documented by a fresh reconcile run.
	f.markHealthy(t)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	target = f.target(t)
	require.NotNil(t, target.ActiveRevisionID)
	runs, err := f.journal.ListRuns(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, "reconcile", runs[0].Kind)
	require.Equal(t, "succeeded", runs[0].Status)
}

// TestResyncEnqueuesOutOfSyncEnvironments pins the environment-level floor:
// the resync ticker re-enqueues every environment whose active revision has
// not reached the promoted target, and leaves settled environments alone.
func TestResyncEnqueuesOutOfSyncEnvironments(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	f.executeDeployment(t)
	f.fake.SetFresh()

	drain := func() {
		for f.kernel.queue.Len() > 0 {
			id, _ := f.kernel.queue.Get()
			f.kernel.queue.Done(id)
			f.kernel.queue.Forget(id)
		}
	}

	// Promoted but not yet active: out of sync, the resync re-enqueues.
	drain()
	f.kernel.resync(ctx)
	require.Equal(t, 1, f.kernel.queue.Len(), "an out-of-sync environment re-enters the queue")

	// Converged to active: in sync, the resync stays quiet.
	drain()
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	f.markHealthy(t)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	drain()
	f.kernel.resync(ctx)
	require.Zero(t, f.kernel.queue.Len(), "a settled environment is not re-enqueued")
}

// The worker's failure backoff is capped at the health-check cadence: this
// pins the exact limiter construction New uses, so a persistently erroring
// environment retries within requeueHealthCheck instead of the exponential
// tail that once parked in-flight rollouts for many minutes.
func TestFailureBackoffCapped(t *testing.T) {
	t.Parallel()
	limiter := workqueue.NewTypedWithMaxWaitRateLimiter(
		workqueue.DefaultTypedControllerRateLimiter[uuid.UUID](), requeueHealthCheck)
	id := uuid.New()
	for range 30 {
		require.LessOrEqual(t, limiter.When(id), requeueHealthCheck)
	}
}

// TestWaitStepReasonUpdates pins the truthful-wait contract: a waiting
// step's reason appends as a fresh log line when it changes across passes,
// stays silent when it repeats, and the step itself stays waiting.
func TestWaitStepReasonUpdates(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{})
	ctx := context.Background()

	run, err := f.journal.CreateRun(ctx, journal.RunInput{
		Kind: "reconcile", ProjectID: f.projectID, EnvironmentID: f.environmentID, Actor: "test",
	})
	require.NoError(t, err)
	require.NoError(t, f.journal.StartRun(ctx, run.ID))
	attachment := &runAttachment{journal: f.journal, run: run}

	attachment.waitStep(ctx, "claim:databases.data", "Provision databases.data", "pool starting")
	attachment.waitStep(ctx, "claim:databases.data", "Provision databases.data", "pool starting")
	attachment.waitStep(ctx, "claim:databases.data", "Provision databases.data", "database applying")

	tree, err := f.journal.RunTree(ctx, run.ID)
	require.NoError(t, err)
	require.Len(t, tree.Steps, 1)
	step := tree.Steps[0].Step
	require.Equal(t, "waiting", step.Status)

	logs, err := f.journal.StepLogs(ctx, step.ID, journal.Cursor{}, 0)
	require.NoError(t, err)
	require.Len(t, logs, 2, "a repeated reason must not append a line")
	require.Equal(t, "pool starting", logs[0].Message)
	require.Equal(t, "database applying", logs[1].Message)
}

// The activation guard: activating a revision that is no longer the target
// is a no-op (0 rows), never a rollback of the newer target.
func TestActivationGuardSupersededTarget(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{})
	ctx := context.Background()
	f.executeDeployment(t)
	target := f.target(t)
	stale := uuid.New()
	rows, err := f.st.SetEnvironmentActiveRevision(ctx, store.SetEnvironmentActiveRevisionParams{
		EnvironmentID:    f.environmentID,
		ActiveRevisionID: &stale,
	})
	require.NoError(t, err)
	require.Zero(t, rows)
	after := f.target(t)
	require.Equal(t, target.TargetRevisionID, after.TargetRevisionID)
	require.Nil(t, after.ActiveRevisionID)
}

// API-only mode: no cluster, the kernel idles and reports unknown.
func TestKernelAPIOnlyMode(t *testing.T) {
	t.Parallel()
	pool := testdb.New(t)
	st := store.NewStore(pool)
	fake := observe.NewFake()
	kernel := New(Deps{Store: st, Observed: fake.Store, Registry: module.NewRegistry()}, Config{})
	require.False(t, kernel.Connected())

	info := kernel.Observation()
	require.Equal(t, "api-only", info.Mode)
	require.False(t, info.Ready)
	require.Equal(t, module.SourceUnknown, info.Source.State)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- kernel.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("API-only kernel did not stop on context cancel")
	}
}
