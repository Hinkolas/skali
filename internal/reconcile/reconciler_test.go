package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/cluster"
	"github.com/Hinkolas/skali/internal/engine"
	"github.com/Hinkolas/skali/internal/engine/enginetest"
	"github.com/Hinkolas/skali/internal/mirror"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/testdb"
)

// stubImporter mimics the mirror importer against the catalog table: it
// upserts registry_images like the real one (the reconciler's resolution
// rules read the catalog), with a salt to simulate upstream tags moving.
type stubImporter struct {
	st   *store.Store
	mu   sync.Mutex
	err  error
	salt string
	// calls counts Import attempts, successful or not.
	calls int
}

func (s *stubImporter) Import(ctx context.Context, reference string) (store.RegistryImage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return store.RegistryImage{}, s.err
	}
	repo, tag, err := mirror.MirrorRepository(reference)
	if err != nil {
		return store.RegistryImage{}, err
	}
	sum := sha256.Sum256([]byte(reference + s.salt))
	id, err := uuid.NewV7()
	if err != nil {
		return store.RegistryImage{}, err
	}
	return s.st.UpsertRegistryImage(ctx, store.UpsertRegistryImageParams{
		ID: id, Repository: repo, Tag: tag,
		Digest: "sha256:" + hex.EncodeToString(sum[:]), SizeBytes: 1,
	})
}

func (s *stubImporter) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *stubImporter) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type harness struct {
	t     *testing.T
	ctx   context.Context
	st    *store.Store
	svc   *Service
	rec   *Reconciler
	imp   *stubImporter
	fakes map[uuid.UUID]*enginetest.Fake
}

func newHarness(t *testing.T) *harness {
	st := store.NewStore(testdb.New(t))
	h := &harness{t: t, ctx: context.Background(), st: st, fakes: map[uuid.UUID]*enginetest.Fake{}}
	h.imp = &stubImporter{st: st, salt: "v1"}
	handleFor := func(n store.Node) cluster.NodeHandle {
		f, ok := h.fakes[n.ID]
		require.True(t, ok, "no fake engine for node %s", n.Name)
		return cluster.LocalNodeHandle(f)
	}
	h.rec = NewReconciler(st, handleFor, h.imp, "registry.local:5000")
	h.svc = NewService(st, nil)
	return h
}

func (h *harness) addNode(name string, roles []string, online bool) store.Node {
	h.t.Helper()
	id, err := uuid.NewV7()
	require.NoError(h.t, err)
	node, err := h.st.CreateNode(h.ctx, store.CreateNodeParams{
		ID: id, Name: name, Roles: roles, AdvertiseAddr: "10.0.0.1:7443",
	})
	require.NoError(h.t, err)
	h.fakes[id] = enginetest.New()
	if online {
		h.setNodeStatus(id, "online")
		node.Status = "online"
	}
	return node
}

func (h *harness) setNodeStatus(id uuid.UUID, status string) {
	h.t.Helper()
	_, err := h.st.Pool.Exec(h.ctx, "UPDATE nodes SET status = $2 WHERE id = $1", id, status)
	require.NoError(h.t, err)
}

// pass runs one reconciler pass and waits for every convergence goroutine to
// drain, so assertions read settled rows.
func (h *harness) pass() {
	h.t.Helper()
	h.rec.pass(h.ctx)
	require.Eventually(h.t, h.rec.idle, 10*time.Second, 2*time.Millisecond)
}

func (h *harness) create(name string, replicas int, mut func(*WorkloadInput)) store.Workload {
	h.t.Helper()
	in := WorkloadInput{
		Name: name, Kind: engine.KindApplication, Image: "alpine:3",
		Replicas: replicas, DesiredState: DesiredRunning,
	}
	if mut != nil {
		mut(&in)
	}
	row, err := h.svc.Create(h.ctx, in)
	require.NoError(h.t, err)
	return row
}

func (h *harness) assignments(id uuid.UUID) []store.WorkloadAssignment {
	h.t.Helper()
	rows, err := h.st.ListAssignmentsForWorkload(h.ctx, id)
	require.NoError(h.t, err)
	return rows
}

func (h *harness) workload(id uuid.UUID) store.Workload {
	h.t.Helper()
	row, err := h.st.GetWorkloadByID(h.ctx, id)
	require.NoError(h.t, err)
	return row
}

func (h *harness) containersOn(nodeID uuid.UUID) []engine.Container {
	h.t.Helper()
	list, err := h.fakes[nodeID].List(h.ctx)
	require.NoError(h.t, err)
	return list
}

// ageBackoffs pushes every workload/assignment next_attempt_at into the
// past, so a pass retries immediately instead of the test sleeping.
func (h *harness) ageBackoffs() {
	h.t.Helper()
	_, err := h.st.Pool.Exec(h.ctx,
		"UPDATE workloads SET next_attempt_at = now() - interval '1 second' WHERE next_attempt_at IS NOT NULL")
	require.NoError(h.t, err)
	_, err = h.st.Pool.Exec(h.ctx,
		"UPDATE workload_assignments SET next_attempt_at = now() - interval '1 second' WHERE next_attempt_at IS NOT NULL")
	require.NoError(h.t, err)
}

func TestHappyPathReplicasAcrossNodes(t *testing.T) {
	h := newHarness(t)
	a := h.addNode("worker-a", []string{"worker"}, true)
	b := h.addNode("worker-b", []string{"worker"}, true)
	w := h.create("blog", 2, func(in *WorkloadInput) {
		in.Constraints = Constraints{NodeRoles: []string{"worker"}}
		in.Spec = Spec{Env: map[string]string{"PORT": "80"}}
	})

	h.pass()

	// The image was auto-imported digest-pinned into the catalog.
	repo, tag, err := mirror.MirrorRepository("alpine:3")
	require.NoError(t, err)
	catalog, err := h.st.GetRegistryImageByRepoTag(h.ctx, store.GetRegistryImageByRepoTagParams{Repository: repo, Tag: tag})
	require.NoError(t, err)
	row := h.workload(w.ID)
	require.Equal(t, catalog.Digest, *row.ResolvedDigest)

	// Both slots ready, on distinct nodes.
	asgs := h.assignments(w.ID)
	require.Len(t, asgs, 2)
	nodesSeen := map[uuid.UUID]bool{}
	for _, asg := range asgs {
		require.Equal(t, PhaseReady, asg.Phase)
		require.Equal(t, row.Generation, asg.Generation)
		require.Nil(t, asg.LastError)
		require.NotNil(t, asg.NodeID)
		nodesSeen[*asg.NodeID] = true
	}
	require.Len(t, nodesSeen, 2, "replicas must land on distinct nodes")

	// Containers run with full identity labels and the mirror-pinned image.
	for i, nodeID := range []uuid.UUID{a.ID, b.ID} {
		ctrs := h.containersOn(nodeID)
		require.Len(t, ctrs, 1, "node %d", i)
		c := ctrs[0]
		require.Equal(t, "running", c.State)
		require.Equal(t, w.ID.String(), c.Labels[engine.LabelWorkload])
		require.NotEmpty(t, c.Labels[engine.LabelConfigHash])
		require.Contains(t, c.Image, "registry.local:5000/"+repo+"@sha256:")

		// Write-through: observed state reflects the create immediately.
		obs, err := h.st.GetNodeContainer(h.ctx, store.GetNodeContainerParams{NodeID: nodeID, ContainerID: c.ID})
		require.NoError(t, err)
		require.Equal(t, "running", obs.State)
	}

	// A second pass is a no-op: nothing pulled or created again.
	pulledA := len(h.fakes[a.ID].Pulled)
	h.pass()
	require.Len(t, h.fakes[a.ID].Pulled, pulledA)
	require.Len(t, h.containersOn(a.ID), 1)
}

func TestImportFailureBackoffPersistsAcrossRestart(t *testing.T) {
	h := newHarness(t)
	h.addNode("worker-a", []string{"worker"}, true)
	h.imp.setErr(fmt.Errorf("upstream on fire"))
	w := h.create("blog", 1, nil)

	h.pass()
	row := h.workload(w.ID)
	require.NotNil(t, row.LastError)
	require.Contains(t, *row.LastError, "upstream on fire")
	require.EqualValues(t, 1, row.Retries)
	require.NotNil(t, row.NextAttemptAt)
	require.True(t, row.NextAttemptAt.After(time.Now()))
	require.Equal(t, 1, h.imp.callCount())

	// A fresh reconciler (master restart) honors the persisted backoff: no
	// new import attempt while next_attempt_at is in the future.
	h.rec = NewReconciler(h.st, h.rec.handleFor, h.imp, h.rec.endpoint)
	h.pass()
	require.Equal(t, 1, h.imp.callCount(), "backoff must survive a restart")

	// Upstream recovers, the backoff ages out, convergence completes.
	h.imp.setErr(nil)
	h.ageBackoffs()
	h.pass()
	row = h.workload(w.ID)
	require.Nil(t, row.LastError)
	require.NotNil(t, row.ResolvedDigest)
	require.Equal(t, PhaseReady, h.assignments(w.ID)[0].Phase)
}

func TestSpecUpdateReplacesContainers(t *testing.T) {
	h := newHarness(t)
	n := h.addNode("worker-a", []string{"worker"}, true)
	w := h.create("blog", 1, nil)
	h.pass()
	before := h.containersOn(n.ID)
	require.Len(t, before, 1)

	_, err := h.svc.Update(h.ctx, w.ID, WorkloadUpdate{
		Spec: &Spec{Env: map[string]string{"NEW": "value"}},
	})
	require.NoError(t, err)
	h.pass()

	after := h.containersOn(n.ID)
	require.Len(t, after, 1)
	require.NotEqual(t, before[0].ID, after[0].ID, "hash drift must replace the container")
	require.Equal(t, "running", after[0].State)
	asg := h.assignments(w.ID)[0]
	require.Equal(t, PhaseReady, asg.Phase)
	require.EqualValues(t, 2, asg.Generation, "assignment reports the converged generation")
}

func TestWorkloadOwnsItsPin(t *testing.T) {
	h := newHarness(t)
	n := h.addNode("worker-a", []string{"worker"}, true)
	w := h.create("blog", 1, nil)
	h.pass()
	before := h.containersOn(n.ID)
	pinned := *h.workload(w.ID).ResolvedDigest

	// A re-import moves the catalog pin (new upstream content). The workload
	// keeps its own digest: no restarts behind the user's back.
	h.imp.salt = "v2"
	_, err := h.imp.Import(h.ctx, "alpine:3")
	require.NoError(t, err)
	h.pass()
	require.Equal(t, pinned, *h.workload(w.ID).ResolvedDigest)
	require.Equal(t, before[0].ID, h.containersOn(n.ID)[0].ID, "container must not churn")

	// Deleting the catalog row is a deliberate re-resolution: the reconciler
	// re-imports, adopts the new digest, and replaces by hash drift.
	repo, tag, err := mirror.MirrorRepository("alpine:3")
	require.NoError(t, err)
	catalog, err := h.st.GetRegistryImageByRepoTag(h.ctx, store.GetRegistryImageByRepoTagParams{Repository: repo, Tag: tag})
	require.NoError(t, err)
	_, err = h.st.DeleteRegistryImageByID(h.ctx, catalog.ID)
	require.NoError(t, err)
	imports := h.imp.callCount()

	h.pass()
	require.Greater(t, h.imp.callCount(), imports)
	require.NotEqual(t, pinned, *h.workload(w.ID).ResolvedDigest)
	require.NotEqual(t, before[0].ID, h.containersOn(n.ID)[0].ID)
}

func TestScaleDownRemovesExcessSlots(t *testing.T) {
	h := newHarness(t)
	a := h.addNode("worker-a", []string{"worker"}, true)
	b := h.addNode("worker-b", []string{"worker"}, true)
	w := h.create("blog", 2, nil)
	h.pass()
	require.Len(t, h.containersOn(a.ID), 1)
	require.Len(t, h.containersOn(b.ID), 1)

	replicas := 1
	_, err := h.svc.Update(h.ctx, w.ID, WorkloadUpdate{Replicas: &replicas})
	require.NoError(t, err)
	h.pass()

	asgs := h.assignments(w.ID)
	require.Len(t, asgs, 1)
	require.EqualValues(t, 0, asgs[0].Ordinal)
	require.Equal(t, 1, len(h.containersOn(a.ID))+len(h.containersOn(b.ID)),
		"the excess replica's container must be removed")
}

func TestConvergenceHealsLostAndStoppedContainers(t *testing.T) {
	h := newHarness(t)
	n := h.addNode("worker-a", []string{"worker"}, true)
	h.create("blog", 1, nil)
	h.pass()
	ctr := h.containersOn(n.ID)[0]

	// Killed and gone (the report would drop the row): redeploy.
	require.NoError(t, h.fakes[n.ID].Remove(h.ctx, ctr.ID, true))
	_, err := h.st.DeleteNodeContainer(h.ctx, store.DeleteNodeContainerParams{NodeID: n.ID, ContainerID: ctr.ID})
	require.NoError(t, err)
	h.pass()
	replaced := h.containersOn(n.ID)
	require.Len(t, replaced, 1)
	require.Equal(t, "running", replaced[0].State)
	require.NotEqual(t, ctr.ID, replaced[0].ID)

	// Exited but present (observed says so): started, not replaced.
	require.NoError(t, h.fakes[n.ID].Stop(h.ctx, replaced[0].ID, 0))
	stopped, err := h.fakes[n.ID].Inspect(h.ctx, replaced[0].ID)
	require.NoError(t, err)
	require.NoError(t, cluster.RecordObservedContainer(h.ctx, h.st, n.ID, stopped))
	h.pass()
	after := h.containersOn(n.ID)
	require.Len(t, after, 1)
	require.Equal(t, replaced[0].ID, after[0].ID, "a present container is started, not replaced")
	require.Equal(t, "running", after[0].State)
}

func TestDesiredStoppedAndBack(t *testing.T) {
	h := newHarness(t)
	n := h.addNode("worker-a", []string{"worker"}, true)
	w := h.create("blog", 1, nil)
	h.pass()

	stoppedState := DesiredStopped
	_, err := h.svc.Update(h.ctx, w.ID, WorkloadUpdate{DesiredState: &stoppedState})
	require.NoError(t, err)
	h.pass()
	require.Equal(t, "exited", h.containersOn(n.ID)[0].State)
	require.Equal(t, PhaseStopped, h.assignments(w.ID)[0].Phase)

	runningState := DesiredRunning
	_, err = h.svc.Update(h.ctx, w.ID, WorkloadUpdate{DesiredState: &runningState})
	require.NoError(t, err)
	h.pass()
	require.Equal(t, "running", h.containersOn(n.ID)[0].State)
	require.Equal(t, PhaseReady, h.assignments(w.ID)[0].Phase)
}

func TestOfflineNodeBlocksThenHeals(t *testing.T) {
	h := newHarness(t)
	n := h.addNode("worker-a", []string{"worker"}, false) // offline
	w := h.create("blog", 1, nil)

	h.pass()
	asg := h.assignments(w.ID)[0]
	require.NotNil(t, asg.NodeID, "an offline node is still placeable")
	require.NotNil(t, asg.LastError)
	require.Contains(t, *asg.LastError, "node offline")
	require.Zero(t, asg.Retries, "blocked must not burn retries")
	require.Empty(t, h.containersOn(n.ID), "no engine calls against an offline node")

	h.setNodeStatus(n.ID, "online")
	h.pass()
	asg = h.assignments(w.ID)[0]
	require.Equal(t, PhaseReady, asg.Phase)
	require.Nil(t, asg.LastError)
	require.Len(t, h.containersOn(n.ID), 1)
}

func TestUnschedulableSurfacesThenHeals(t *testing.T) {
	h := newHarness(t)
	h.addNode("worker-a", []string{"worker"}, true)
	h.addNode("edge-a", []string{"edge"}, true)
	w := h.create("blog", 2, func(in *WorkloadInput) {
		in.Constraints = Constraints{NodeRoles: []string{"worker"}}
	})

	h.pass()
	asgs := h.assignments(w.ID)
	require.Len(t, asgs, 2)
	require.Equal(t, PhaseReady, asgs[0].Phase)
	require.Equal(t, PhaseUnschedulable, asgs[1].Phase)
	require.Contains(t, *asgs[1].LastError, "no eligible node")

	h.addNode("worker-b", []string{"worker"}, true)
	h.pass()
	asgs = h.assignments(w.ID)
	require.Equal(t, PhaseReady, asgs[1].Phase)
	require.Nil(t, asgs[1].LastError)
}

func TestAdoptionAfterLostAssignments(t *testing.T) {
	h := newHarness(t)
	a := h.addNode("worker-a", []string{"worker"}, true)
	h.addNode("worker-b", []string{"worker"}, true)
	w := h.create("blog", 1, nil)
	h.pass()
	ctr := h.containersOn(a.ID)[0]
	pulled := len(h.fakes[a.ID].Pulled)

	// Simulate lost reconciler bookkeeping (the workload rows survived, the
	// assignment rows did not — worse than any real failover). Everything
	// needed to adopt lives in the container's labels.
	_, err := h.st.Pool.Exec(h.ctx, "DELETE FROM workload_assignments WHERE workload_id = $1", w.ID)
	require.NoError(t, err)

	h.pass()
	asgs := h.assignments(w.ID)
	require.Len(t, asgs, 1)
	require.Equal(t, PhaseReady, asgs[0].Phase)
	require.Equal(t, a.ID, *asgs[0].NodeID, "placement must adopt the node already running the slot")
	require.Equal(t, ctr.ID, *asgs[0].ContainerID)
	require.Len(t, h.containersOn(a.ID), 1)
	require.Len(t, h.fakes[a.ID].Pulled, pulled, "adoption needs zero engine work")
}

func TestOrphanGC(t *testing.T) {
	h := newHarness(t)
	a := h.addNode("worker-a", []string{"worker"}, true)
	b := h.addNode("worker-b", []string{"worker"}, true)
	w := h.create("blog", 1, func(in *WorkloadInput) {
		in.Constraints = Constraints{NodeIDs: []uuid.UUID{a.ID}}
	})
	h.pass()

	// A leftover twin of slot 0 sits on node B (e.g. relocation remains).
	id := h.fakes[b.ID].Add(engine.Container{
		Name: "blog-0", Image: "x", State: "running",
		Labels: map[string]string{
			engine.LabelManaged: "true", engine.LabelKind: engine.KindApplication,
			engine.LabelWorkload: w.ID.String(), engine.LabelInstance: "0",
		},
	})
	twin, err := h.fakes[b.ID].Inspect(h.ctx, id)
	require.NoError(t, err)
	require.NoError(t, cluster.RecordObservedContainer(h.ctx, h.st, b.ID, twin))

	h.pass()
	require.Empty(t, h.containersOn(b.ID), "the unwanted twin is collected")
	require.Len(t, h.containersOn(a.ID), 1, "the real slot is untouched")
}

func TestTeardownBlocksOnOfflineNode(t *testing.T) {
	h := newHarness(t)
	a := h.addNode("worker-a", []string{"worker"}, true)
	b := h.addNode("worker-b", []string{"worker"}, true)
	w := h.create("blog", 2, nil)
	h.pass()

	h.setNodeStatus(b.ID, "offline")
	require.NoError(t, h.svc.Delete(h.ctx, w.ID))
	h.pass()

	// Node A's slot is gone; node B's blocks completion, visibly.
	require.Empty(t, h.containersOn(a.ID))
	asgs := h.assignments(w.ID)
	require.Len(t, asgs, 1)
	require.Equal(t, PhaseRemoving, asgs[0].Phase)
	require.Contains(t, *asgs[0].LastError, "node offline")
	_, err := h.st.GetWorkloadByID(h.ctx, w.ID)
	require.NoError(t, err, "the workload row must survive until every slot is gone")

	// Deleting the node is the escape hatch: its observations cascade away
	// and the teardown completes.
	_, err = h.st.DeleteNodeByID(h.ctx, b.ID)
	require.NoError(t, err)
	h.pass()
	require.Empty(t, h.assignments(w.ID))
	_, err = h.st.GetWorkloadByID(h.ctx, w.ID)
	require.Error(t, err, "workload row hard-deleted after teardown")
}

func TestForeignNameSquatterSurfacesConflict(t *testing.T) {
	h := newHarness(t)
	n := h.addNode("worker-a", []string{"worker"}, true)
	// Someone else runs a container with our target name, without our labels.
	h.fakes[n.ID].Add(engine.Container{Name: "blog-0", Image: "x", State: "running",
		Labels: map[string]string{engine.LabelManaged: "true", engine.LabelKind: engine.KindSystem}})
	w := h.create("blog", 1, nil)

	h.pass()
	asg := h.assignments(w.ID)[0]
	require.NotNil(t, asg.LastError)
	require.Contains(t, *asg.LastError, "name already in use")
	require.EqualValues(t, 1, asg.Retries)
	require.NotNil(t, asg.NextAttemptAt, "conflicts back off instead of hammering")
}

func TestReplicasZeroConvergesToNothing(t *testing.T) {
	h := newHarness(t)
	n := h.addNode("worker-a", []string{"worker"}, true)
	w := h.create("blog", 1, nil)
	h.pass()
	require.Len(t, h.containersOn(n.ID), 1)

	zero := 0
	_, err := h.svc.Update(h.ctx, w.ID, WorkloadUpdate{Replicas: &zero})
	require.NoError(t, err)
	h.pass()
	require.Empty(t, h.containersOn(n.ID))
	require.Empty(t, h.assignments(w.ID))
	_, err = h.st.GetWorkloadByID(h.ctx, w.ID)
	require.NoError(t, err, "replicas=0 keeps the workload, unlike delete")
}
