package substrate

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/reconcile"
	"github.com/Hinkolas/skali/internal/revision"
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
	// secretVersion is the counter behind Secret resource versions.
	secretVersion int
	// policies retains the last applied NetworkPolicy per namespace/name,
	// so tests can read what a pass admitted.
	policies map[string]*networkingv1.NetworkPolicy

	// poolPhase and poolReady shape the ClusterGVR read; an empty phase
	// reads as not created yet.
	poolPhase string
	poolReady int64
	// databaseApplied shapes the DatabaseGVR read.
	databaseApplied bool
	// roleStatus shapes the ClusterGVR read's managedRolesStatus: the
	// roles CNPG reports reconciled and the password Secret resource
	// version it applied for each.
	roleStatus map[string]string
	// execs records every ExecInPod call; execErr is returned by each;
	// answer, when set, supplies the stdout of a call (role queries).
	execs   []fakeExec
	execErr error
	answer  func(command []string) string
	// deleted records every Delete call's object name.
	deleted []string
}

// fakeExec is one recorded ExecInPod call.
type fakeExec struct {
	selector  string
	container string
	command   []string
}

func (f *fakeCluster) set(phase string, ready int64, applied bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.poolPhase, f.poolReady, f.databaseApplied = phase, ready, applied
}

// reconcileRole makes the ClusterGVR read report the role reconciled with
// the password Secret at the given resource version.
func (f *fakeCluster) reconcileRole(role, secretVersion string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.roleStatus == nil {
		f.roleStatus = map[string]string{}
	}
	f.roleStatus[role] = secretVersion
}

func (f *fakeCluster) ExecInPod(_ context.Context, _ string, selector, container string, command []string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execs = append(f.execs, fakeExec{selector: selector, container: container, command: append([]string(nil), command...)})
	if f.execErr != nil {
		return "", f.execErr
	}
	if f.answer != nil {
		return f.answer(command), nil
	}
	// Without an answer the pool holds no role: counts are zero, lookups
	// find nothing.
	if strings.HasPrefix(command[len(command)-1], "SELECT count(*)") {
		return "0", nil
	}
	return "", nil
}

// scripts returns the SQL of every recorded exec, in order.
func (f *fakeCluster) scripts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.execs))
	for _, call := range f.execs {
		out = append(out, call.command[len(call.command)-1])
	}
	return out
}

func (f *fakeCluster) ApplyAs(_ context.Context, obj runtime.Object, _ string, _ bool) (kube.ApplyResult, error) {
	if policy, ok := obj.(*networkingv1.NetworkPolicy); ok {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.policies == nil {
			f.policies = map[string]*networkingv1.NetworkPolicy{}
		}
		f.policies[policy.Namespace+"/"+policy.Name] = policy.DeepCopy()
		return kube.ApplyResult{Changed: true}, nil
	}
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
		copied.StringData = nil
		f.storeSecret(copied)
	}
	return kube.ApplyResult{Changed: true}, nil
}

// storeSecret retains a Secret under a fresh resource version, the
// optimistic-concurrency token UpdateSecret checks like the API server.
func (f *fakeCluster) storeSecret(secret *corev1.Secret) {
	if f.secrets == nil {
		f.secrets = map[string]*corev1.Secret{}
	}
	f.secretVersion++
	secret.ResourceVersion = strconv.Itoa(f.secretVersion)
	f.secrets[secret.Namespace+"/"+secret.Name] = secret
}

func (f *fakeCluster) UpdateSecret(_ context.Context, secret *corev1.Secret) (*corev1.Secret, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := secret.Namespace + "/" + secret.Name
	stored, ok := f.secrets[key]
	if !ok {
		return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, secret.Name)
	}
	if secret.ResourceVersion != stored.ResourceVersion {
		return nil, apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, secret.Name,
			errors.New("the object has been modified"))
	}
	copied := secret.DeepCopy()
	if copied.Data == nil {
		copied.Data = map[string][]byte{}
	}
	for k, value := range copied.StringData {
		copied.Data[k] = []byte(value)
	}
	copied.StringData = nil
	f.storeSecret(copied)
	return copied.DeepCopy(), nil
}

func (f *fakeCluster) Delete(_ context.Context, ref kube.ObjectRef) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, ref.Name)
	if ref.GVK.Kind == "Secret" {
		if _, ok := f.secrets[ref.Namespace+"/"+ref.Name]; ok {
			delete(f.secrets, ref.Namespace+"/"+ref.Name)
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeCluster) ListPods(context.Context, string, string) ([]corev1.Pod, error) {
	return nil, nil
}

func (f *fakeCluster) GetSecret(_ context.Context, namespace, name string) (*corev1.Secret, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if secret, ok := f.secrets[namespace+"/"+name]; ok {
		return secret.DeepCopy(), nil
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
		status := map[string]any{
			"phase":          f.poolPhase,
			"readyInstances": f.poolReady,
		}
		if len(f.roleStatus) > 0 {
			reconciled := make([]any, 0, len(f.roleStatus))
			passwords := map[string]any{}
			for role, version := range f.roleStatus {
				reconciled = append(reconciled, role)
				passwords[role] = map[string]any{"resourceVersion": version}
			}
			status["managedRolesStatus"] = map[string]any{
				"byStatus":       map[string]any{"reconciled": reconciled},
				"passwordStatus": passwords,
			}
		}
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "postgresql.cnpg.io/v1",
			"kind":       "Cluster",
			"metadata":   map[string]any{"name": name, "namespace": namespace},
			"status":     status,
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

func (f *fakeCluster) PodCIDRs(context.Context) ([]string, error) {
	return nil, nil
}

// policy returns the last applied NetworkPolicy of that name in the
// platform namespace, nil when none was applied.
func (f *fakeCluster) policy(name string) *networkingv1.NetworkPolicy {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.policies[Namespace+"/"+name]
}

// settleFixture is the shared scaffolding: a real claims database, a fake
// cluster, and a controller whose environment pokes are captured.
type settleFixture struct {
	db          *dbstore.Service
	fake        *fakeCluster
	control     *Controller
	projectID   uuid.UUID
	projectName string
	envID       uuid.UUID
	claim       *store.DatabaseClaim
	poked       *[]uuid.UUID
}

func newSettleFixture(t *testing.T) *settleFixture {
	t.Helper()
	// Role drops wait for the pool to settle in production; the fake pool
	// has nothing to settle.
	roleSettleDelay = 0
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
		projectID: proj.ID, projectName: proj.Name,
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

// ensureInput is the kernel's view of one revision declaring the fixture's
// database with the given extensions.
func (fx *settleFixture) ensureInput(extensions ...string) reconcile.ClaimEnsureInput {
	return reconcile.ClaimEnsureInput{
		ProjectID:     fx.projectID,
		EnvironmentID: fx.envID,
		Revision: &revision.Revision{
			Project:     fx.projectName,
			Environment: "production",
			Definition: compiler.ProjectDefinition{
				Databases: map[string]compiler.DatabaseClaim{
					"data": {Engine: "postgres", Version: "17", Isolation: "project",
						Availability: "single", Extensions: extensions},
				},
			},
		},
	}
}

// TestEnsureWithholdsReadinessUntilExtensionsApply pins the rollout gate for
// an extension requested on a running database: the claim stays provisioned
// (phases never regress) yet Ensure reports it not ready until a pass applied
// the new list and CNPG confirmed it, so a release command that depends on
// the extension never runs ahead of it.
func TestEnsureWithholdsReadinessUntilExtensionsApply(t *testing.T) {
	fx := newSettleFixture(t)
	ctx := context.Background()
	fx.fake.set(clusterHealthyPhase, 1, true)
	_, phase := fx.pass(t)
	require.Equal(t, claim.PhaseProvisioned, phase)

	states, err := fx.control.Ensure(ctx, fx.ensureInput())
	require.NoError(t, err)
	require.Len(t, states, 1)
	require.True(t, states[0].Provisioned, "an unchanged provisioned claim is ready")

	// The operator has not yet created the extension: readiness drops with
	// the pending reason, and the claim still reports provisioned in phase.
	fx.fake.set(clusterHealthyPhase, 1, false)
	states, err = fx.control.Ensure(ctx, fx.ensureInput("vector"))
	require.NoError(t, err)
	require.False(t, states[0].Provisioned)
	require.Equal(t, extensionsPendingReason, states[0].Waiting)
	current, err := fx.db.GetClaim(ctx, fx.claim.ID)
	require.NoError(t, err)
	require.Equal(t, claim.PhaseProvisioned, claim.Phase(current.Phase))
	require.Equal(t, []string{"vector"}, dbstore.Extensions(*current))

	// A pass that waits on the Database CR leaves the gate closed.
	requeue, _ := fx.pass(t)
	require.Equal(t, requeueWait, requeue)
	states, err = fx.control.Ensure(ctx, fx.ensureInput("vector"))
	require.NoError(t, err)
	require.False(t, states[0].Provisioned)
	require.Equal(t, extensionsPendingReason, states[0].Waiting)

	// Once CNPG applied it, the pass that observes it settles the marker.
	fx.fake.set(clusterHealthyPhase, 1, true)
	requeue, _ = fx.pass(t)
	require.Zero(t, requeue)
	states, err = fx.control.Ensure(ctx, fx.ensureInput("vector"))
	require.NoError(t, err)
	require.True(t, states[0].Provisioned)
	require.Empty(t, states[0].Waiting)
}

// TestExtensionsPendingSettlesOnlyOnTheAppliedEncoding pins the race guard:
// a pass that applied an older list must not clear a newer request, and a
// released claim drops its marker outright.
func TestExtensionsPendingSettlesOnlyOnTheAppliedEncoding(t *testing.T) {
	fx := newSettleFixture(t)
	id := fx.claim.ID
	desired := dbstore.MarshalExtensions([]string{"pg_trgm", "vector"})
	fx.control.markExtensionsPending(id, desired)
	require.True(t, fx.control.extensionsPending(id))

	fx.control.settleExtensions(id, dbstore.MarshalExtensions([]string{"pg_trgm"}))
	require.True(t, fx.control.extensionsPending(id), "an older applied list must not settle the request")

	fx.control.settleExtensions(id, dbstore.MarshalExtensions([]string{"vector", "pg_trgm"}))
	require.False(t, fx.control.extensionsPending(id), "the encoding is canonical regardless of input order")

	fx.control.markExtensionsPending(id, desired)
	fx.control.clearExtensions(id)
	require.False(t, fx.control.extensionsPending(id))
}
