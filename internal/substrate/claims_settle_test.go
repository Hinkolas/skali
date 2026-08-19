package substrate

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/cnpg"
	"github.com/Hinkolas/skali/internal/testdb"
)

// fakeCluster satisfies Cluster with canned CNPG status objects so claim
// passes run against real claim rows without a live cluster. Applied Secrets
// are retained so credential reads stay stable across passes.
type fakeCluster struct {
	mu      sync.Mutex
	secrets map[string]*corev1.Secret

	// poolPhase and poolReady shape the ClusterGVR read; an empty phase
	// reads as not created yet.
	poolPhase string
	poolReady int64
	// databaseApplied shapes the DatabaseGVR read.
	databaseApplied bool
}

func (f *fakeCluster) set(phase string, ready int64, applied bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.poolPhase, f.poolReady, f.databaseApplied = phase, ready, applied
}

func (f *fakeCluster) ApplyAs(_ context.Context, obj runtime.Object, _ string, _ bool) (kube.ApplyResult, error) {
	if secret, ok := obj.(*corev1.Secret); ok {
		f.mu.Lock()
		defer f.mu.Unlock()
		copied := secret.DeepCopy()
		if copied.Data == nil {
			copied.Data = map[string][]byte{}
		}
		for key, value := range copied.StringData {
			copied.Data[key] = []byte(value)
		}
		if f.secrets == nil {
			f.secrets = map[string]*corev1.Secret{}
		}
		f.secrets[secret.Namespace+"/"+secret.Name] = copied
	}
	return kube.ApplyResult{Changed: true}, nil
}

func (f *fakeCluster) Delete(context.Context, kube.ObjectRef) (bool, error) {
	return false, nil
}

func (f *fakeCluster) GetSecret(_ context.Context, namespace, name string) (*corev1.Secret, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if secret, ok := f.secrets[namespace+"/"+name]; ok {
		return secret, nil
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, name)
}

func (f *fakeCluster) GetObject(_ context.Context, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch gvr {
	case cnpg.ClusterGVR:
		if f.poolPhase == "" {
			return nil, apierrors.NewNotFound(schema.GroupResource{Group: gvr.Group, Resource: gvr.Resource}, name)
		}
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "postgresql.cnpg.io/v1",
			"kind":       "Cluster",
			"metadata":   map[string]any{"name": name, "namespace": namespace},
			"status": map[string]any{
				"phase":          f.poolPhase,
				"readyInstances": f.poolReady,
			},
		}}, nil
	case cnpg.DatabaseGVR:
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "postgresql.cnpg.io/v1",
			"kind":       "Database",
			"metadata":   map[string]any{"name": name, "namespace": namespace},
			"status":     map[string]any{"applied": f.databaseApplied},
		}}, nil
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{Group: gvr.Group, Resource: gvr.Resource}, name)
}

func (f *fakeCluster) ProxyCIDRs(context.Context) ([]string, error) {
	return nil, nil
}

// settleFixture is the shared scaffolding: a real claims database, a fake
// cluster, and a controller whose environment pokes are captured.
type settleFixture struct {
	db      *dbstore.Service
	fake    *fakeCluster
	control *Controller
	envID   uuid.UUID
	claim   *store.DatabaseClaim
	poked   *[]uuid.UUID
}

func newSettleFixture(t *testing.T) *settleFixture {
	t.Helper()
	ctx := context.Background()
	pool := testdb.New(t)
	st := store.NewStore(pool)
	dbSvc := dbstore.New(st)
	projects := project.New(st)

	suffix := uuid.Must(uuid.NewV7()).String()[24:]
	proj, err := projects.Create(ctx, "demo"+suffix, "", uuid.Nil)
	require.NoError(t, err)
	env, err := projects.CreateEnvironment(ctx, proj.ID, "production", project.EnvironmentOptions{})
	require.NoError(t, err)

	fake := &fakeCluster{}
	poked := []uuid.UUID{}
	controller := New(Deps{
		DB:       dbSvc,
		Cluster:  fake,
		Observed: observe.NewStore(nil),
		Enqueue:  func(id uuid.UUID) { poked = append(poked, id) },
	}, Config{Managed: false})

	owner := dbstore.ServiceOwner(proj.ID, env.ID, proj.Name, "production", "data")
	created, err := dbSvc.EnsureClaim(ctx, owner, dbstore.ClaimSpec{
		Engine: "postgres", Major: 17,
		Isolation: "project", Availability: "single",
	})
	require.NoError(t, err)

	return &settleFixture{
		db: dbSvc, fake: fake, control: controller,
		envID: env.ID, claim: created, poked: &poked,
	}
}

// pass runs one worker pass and enforces the queue invariant: a pass that
// returns neither an error nor a requeue must have settled the claim in a
// terminal phase.
func (fx *settleFixture) pass(t *testing.T) (time.Duration, claim.Phase) {
	t.Helper()
	ctx := context.Background()
	requeue, err := fx.control.reconcileClaim(ctx, fx.claim.ID)
	require.NoError(t, err)
	current, err := fx.db.GetClaim(ctx, fx.claim.ID)
	require.NoError(t, err)
	phase := claim.Phase(current.Phase)
	if requeue == 0 {
		require.Contains(t, []claim.Phase{claim.PhaseProvisioned, claim.PhaseReleased}, phase,
			"pass abandoned unsettled claim: phase=%s wait=%q", phase, fx.control.WaitingReason(fx.claim.ID))
	}
	return requeue, phase
}

// TestClaimSettlesInOnePass pins the incident-class regression: when every
// dependency is ready, the pass that binds a pending claim must also carry it
// to provisioned and poke the environment, not stop at the mid-pass bind.
func TestClaimSettlesInOnePass(t *testing.T) {
	fx := newSettleFixture(t)
	fx.fake.set(clusterHealthyPhase, 1, true)

	requeue, phase := fx.pass(t)
	require.Equal(t, claim.PhaseProvisioned, phase,
		"one pass with a ready substrate must settle the claim")
	require.Zero(t, requeue)
	require.Contains(t, *fx.poked, fx.envID,
		"provisioning must poke the environment reconciler")
	require.Empty(t, fx.control.WaitingReason(fx.claim.ID))
}

// TestClaimWaitingKeepsRequeueAndPokes pins two queue behaviors: a waiting
// claim never leaves the queue, and any phase movement pokes the environment
// so its wait text refreshes even without a completed transition.
func TestClaimWaitingKeepsRequeueAndPokes(t *testing.T) {
	fx := newSettleFixture(t)
	fx.fake.set("Setting up primary", 0, false)

	requeue, phase := fx.pass(t)
	require.Equal(t, requeueWait, requeue,
		"a waiting claim must stay on the queue")
	require.Equal(t, claim.PhaseBound, phase, "place binds mid-pass")
	require.Contains(t, fx.control.WaitingReason(fx.claim.ID), "pool")
	require.Contains(t, *fx.poked, fx.envID,
		"the pending to bound movement must refresh the environment's view")

	fx.fake.set(clusterHealthyPhase, 1, true)
	_, phase = fx.pass(t)
	require.Equal(t, claim.PhaseProvisioned, phase)
	require.GreaterOrEqual(t, len(*fx.poked), 2,
		"the provisioned transition must poke the environment again")
}
