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

	"github.com/Hinkolas/skali/internal/artifactstore"
	"github.com/Hinkolas/skali/internal/deploy"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/module/apptest"
	"github.com/Hinkolas/skali/internal/module/database"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/testdb"
	"github.com/Hinkolas/skali/internal/values"
	"github.com/Hinkolas/skali/internal/valuestore"
)

const kernelManifest = `version: "1"
name: demo
values:
  SESSION_SECRET:
    secret: true
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
}

func newFakeCluster() *fakeCluster {
	return &fakeCluster{applied: make(map[string]bool)}
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
	require.NoError(t, registry.Register(apptest.Module{}))
	require.NoError(t, registry.Register(database.Module{}))
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
	}, cfg)
	deploySvc.SetEnqueuer(kernel)

	proj, err := projects.Create(ctx, "demo", "")
	require.NoError(t, err)
	env, err := projects.CreateEnvironment(ctx, proj.ID, "production")
	require.NoError(t, err)

	return &kernelFixture{
		st: st, kernel: kernel, journal: journalSvc, deploy: deploySvc,
		fake: fake, cluster: cluster,
		projectID: proj.ID, environmentID: env.ID,
		namespace: "skali-demo-production",
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
	draft, err := projects.SubmitDraft(ctx, f.projectID, project.DraftSubmission{
		Source: []byte(manifestSource), Format: "yaml", ExpectedVersion: 0,
	})
	require.NoError(t, err)
	row, err := f.st.GetDefinitionVersionByHash(ctx, store.GetDefinitionVersionByHashParams{
		ProjectID: f.projectID, DefinitionHash: draft.Hash,
	})
	require.NoError(t, err)
	valueSvc, err := valuestore.New(f.st, strings.Repeat("k", 32))
	require.NoError(t, err)
	candidate, err := valueSvc.Stage(ctx, f.environmentID, values.Resolved{
		Plain:  map[string]string{"APP_DOMAIN": "demo.example.com"},
		Secret: map[string]string{"SESSION_SECRET": "kernel-plant-value"},
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

func (f *kernelFixture) markHealthy(t *testing.T) {
	t.Helper()
	target := f.target(t)
	require.NotNil(t, target.TargetRevisionID)
	row, err := f.st.GetRevisionByID(context.Background(), *target.TargetRevisionID)
	require.NoError(t, err)
	f.fake.SetWorkload(f.environmentID, f.namespace, "demo-web", "web", row.Checksum[:16],
		module.WorkloadStatus{Desired: 1, Ready: 1})
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
	require.Contains(t, ops, "apply Deployment/"+f.namespace+"/demo-web")
	require.Contains(t, ops, "apply Service/"+f.namespace+"/demo-web")

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
// stays; a later recovery still activates (level-triggered, section 8.4;
// automatic fallback is R3).
func TestReconcileDeadlineFailsRunKeepsTarget(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Nanosecond})
	ctx := context.Background()
	result := f.executeDeployment(t)
	f.fake.SetFresh()
	f.fake.SetWorkload(f.environmentID, f.namespace, "demo-web", "web", "",
		module.WorkloadStatus{Desired: 1, Ready: 0})

	requeue, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Zero(t, requeue)

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
