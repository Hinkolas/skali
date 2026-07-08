package reconcile

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/cluster"
	"github.com/Hinkolas/skali/internal/engine"
	"github.com/Hinkolas/skali/internal/mirror"
	"github.com/Hinkolas/skali/internal/store"
)

// Assignment phases. Progress markers for in-flight convergence — observed
// truth stays in node_containers. There is no error phase: an error is
// last_error + next_attempt_at on whatever phase is stuck.
const (
	PhasePending       = "pending"
	PhaseUnschedulable = "unschedulable"
	PhasePulling       = "pulling"
	PhaseDeploying     = "deploying"
	PhaseStopping      = "stopping"
	PhaseReady         = "ready"
	PhaseStopped       = "stopped"
	PhaseRemoving      = "removing"
)

// deployTimeout bounds a create round trip; the image was just pulled, so
// this is container setup, not transfer.
const deployTimeout = 2 * time.Minute

// converge drives one workload toward its desired state: resolve the image
// through the mirror, plan assignments, then converge every slot in parallel
// (slots live on distinct nodes). Every step is idempotent and every cursor
// (phases, retries, backoff) is a row — a crash or failover resumes from
// exactly where the rows say.
func (r *Reconciler) converge(ctx context.Context, w *workload, obs []observedContainer, nodes map[uuid.UUID]store.Node, slotCount map[uuid.UUID]int) {
	// Re-read the row: the pass snapshot may predate a user update.
	row, err := r.st.GetWorkloadByID(ctx, w.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return
	}
	if err != nil {
		slog.WarnContext(ctx, "reconcile: refetch workload", "workload", w.Name, "err", err)
		return
	}
	if w, err = decodeWorkload(row); err != nil {
		slog.ErrorContext(ctx, "reconcile: undecodable workload", "workload", row.Name, "err", err)
		return
	}

	if w.DesiredState == DesiredDeleting {
		r.teardown(ctx, w, obs, nodes)
		return
	}
	if !r.resolveImage(ctx, w) {
		return // assignments wait while the image is unresolved
	}
	asgs, ok := r.plan(ctx, w, obs, nodes, slotCount)
	if !ok {
		return
	}

	var wg sync.WaitGroup
	for _, a := range asgs {
		if a.Ordinal >= w.Replicas {
			continue // scale-down slots were handled by plan
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.convergeSlot(ctx, w, a, obs, nodes)
		}()
	}
	wg.Wait()
}

// resolveImage makes sure the workload has a digest pin the mirror serves.
// The workload owns its pin: while the catalog still has the (repository,
// tag) row, a re-import that moved the catalog pin does NOT restart
// containers — picking it up is an explicit user update. A missing catalog
// row (never imported, or deliberately deleted) imports fresh, which may
// move the digest and replace containers by hash drift.
func (r *Reconciler) resolveImage(ctx context.Context, w *workload) bool {
	repo, tag, err := mirror.MirrorRepository(w.Image)
	if err != nil {
		// The API validates references; a row that fails here is a bug.
		slog.ErrorContext(ctx, "reconcile: unresolvable image reference",
			"workload", w.Name, "image", w.Image, "err", err)
		return false
	}

	catalog, err := r.st.GetRegistryImageByRepoTag(ctx, store.GetRegistryImageByRepoTagParams{
		Repository: repo, Tag: tag,
	})
	switch {
	case err == nil:
		if deref(w.ResolvedDigest) != "" && deref(w.ResolvedRepository) == repo {
			return true // pin held and still served
		}
		if err := r.setResolved(ctx, w, catalog.Repository, catalog.Digest); err != nil {
			slog.WarnContext(ctx, "reconcile: record image pin", "workload", w.Name, "err", err)
			return false
		}
		return true
	case !errors.Is(err, pgx.ErrNoRows):
		slog.WarnContext(ctx, "reconcile: catalog lookup", "workload", w.Name, "err", err)
		return false
	}

	// Not in the catalog: auto-import, behind the workload-level backoff.
	if w.NextAttemptAt != nil && time.Now().Before(*w.NextAttemptAt) {
		return false
	}
	ictx, cancel := context.WithTimeout(ctx, imageResolveTimeout)
	defer cancel()
	imported, err := r.importer.Import(ictx, w.Image)
	if err != nil {
		if w.LastError == nil {
			slog.WarnContext(ctx, "reconcile: image import failing",
				"workload", w.Name, "image", w.Image, "err", err)
		}
		msg := err.Error()
		next := backoffAfter(w.Retries)
		if serr := r.st.SetWorkloadError(ctx, store.SetWorkloadErrorParams{
			ID: w.ID, LastError: &msg, Retries: w.Retries + 1, NextAttemptAt: &next,
		}); serr != nil {
			slog.WarnContext(ctx, "reconcile: record workload error", "workload", w.Name, "err", serr)
		}
		return false
	}
	if w.LastError != nil {
		slog.InfoContext(ctx, "reconcile: image import recovered", "workload", w.Name, "image", w.Image)
	}
	if err := r.setResolved(ctx, w, imported.Repository, imported.Digest); err != nil {
		slog.WarnContext(ctx, "reconcile: record image pin", "workload", w.Name, "err", err)
		return false
	}
	return true
}

func (r *Reconciler) setResolved(ctx context.Context, w *workload, repo, digest string) error {
	if err := r.st.SetWorkloadResolvedImage(ctx, store.SetWorkloadResolvedImageParams{
		ID: w.ID, ResolvedRepository: &repo, ResolvedDigest: &digest,
	}); err != nil {
		return err
	}
	w.ResolvedRepository, w.ResolvedDigest = &repo, &digest
	w.LastError, w.NextAttemptAt, w.Retries = nil, nil, 0
	slog.InfoContext(ctx, "reconcile: image resolved",
		"workload", w.Name, "image", w.Image, "digest", digest)
	return nil
}

// plan reconciles the assignment rows themselves: one per ordinal below
// replicas, scale-down slots converged to absence, unplaced slots assigned a
// node. Returns the fresh rows.
func (r *Reconciler) plan(ctx context.Context, w *workload, obs []observedContainer, nodes map[uuid.UUID]store.Node, slotCount map[uuid.UUID]int) ([]store.WorkloadAssignment, bool) {
	asgs, err := r.st.ListAssignmentsForWorkload(ctx, w.ID)
	if err != nil {
		slog.WarnContext(ctx, "reconcile: list workload assignments", "workload", w.Name, "err", err)
		return nil, false
	}

	have := make(map[int32]bool, len(asgs))
	for _, a := range asgs {
		have[a.Ordinal] = true
	}
	changed := false
	inserted := false
	for i := int32(0); i < w.Replicas; i++ {
		if have[i] {
			continue
		}
		if err := r.st.InsertWorkloadAssignment(ctx, store.InsertWorkloadAssignmentParams{
			WorkloadID: w.ID, Ordinal: i, ContainerName: containerName(w.Name, i),
		}); err != nil {
			slog.WarnContext(ctx, "reconcile: insert assignment", "workload", w.Name, "ordinal", i, "err", err)
			return nil, false
		}
		inserted = true
	}
	if inserted {
		// Placement below must see the fresh rows.
		changed = true
		if asgs, err = r.st.ListAssignmentsForWorkload(ctx, w.ID); err != nil {
			slog.WarnContext(ctx, "reconcile: relist workload assignments", "workload", w.Name, "err", err)
			return nil, false
		}
	}
	taken := make(map[uuid.UUID]bool)
	for _, a := range asgs {
		if a.NodeID != nil && a.Ordinal < w.Replicas {
			taken[*a.NodeID] = true
		}
	}

	for _, a := range asgs {
		if a.Ordinal < w.Replicas {
			continue
		}
		// Scale-down: converge the excess slot to absence now.
		if r.removeSlot(ctx, w, a, obs, nodes) {
			changed = true
		}
	}

	// Place unplaced slots: distinct nodes per workload (sidesteps host-port
	// self-conflicts; relaxing later is an algorithm change, not schema).
	for _, a := range asgs {
		if a.Ordinal >= w.Replicas || a.NodeID != nil {
			continue
		}
		node, msg, ok := pickNode(w, a.Ordinal, obs, nodes, slotCount, taken)
		if !ok {
			if a.Phase != PhaseUnschedulable || deref(a.LastError) != msg {
				slog.WarnContext(ctx, "reconcile: unschedulable",
					"workload", w.Name, "ordinal", a.Ordinal, "reason", msg)
				if err := r.st.MarkAssignmentUnschedulable(ctx, store.MarkAssignmentUnschedulableParams{
					WorkloadID: w.ID, Ordinal: a.Ordinal, LastError: &msg,
				}); err != nil {
					slog.WarnContext(ctx, "reconcile: mark unschedulable", "workload", w.Name, "err", err)
				}
			}
			continue
		}
		if err := r.st.AssignAssignmentNode(ctx, store.AssignAssignmentNodeParams{
			WorkloadID: w.ID, Ordinal: a.Ordinal, NodeID: &node.ID,
		}); err != nil {
			slog.WarnContext(ctx, "reconcile: assign node", "workload", w.Name, "err", err)
			return nil, false
		}
		taken[node.ID] = true
		slotCount[node.ID]++
		changed = true
	}

	if changed {
		if asgs, err = r.st.ListAssignmentsForWorkload(ctx, w.ID); err != nil {
			slog.WarnContext(ctx, "reconcile: relist workload assignments", "workload", w.Name, "err", err)
			return nil, false
		}
	}
	return asgs, true
}

// pickNode selects a node for one slot: constraints filtered, distinct from
// the workload's other slots, preferring a node that already runs this exact
// slot (adoption after failovers), then online > emptiest > name.
func pickNode(w *workload, ordinal int32, obs []observedContainer, nodes map[uuid.UUID]store.Node, slotCount map[uuid.UUID]int, taken map[uuid.UUID]bool) (store.Node, string, bool) {
	matching := 0
	var eligible []store.Node
	for _, n := range nodes {
		if !constraintsMatch(&w.constraints, n) {
			continue
		}
		matching++
		if !taken[n.ID] {
			eligible = append(eligible, n)
		}
	}
	if len(eligible) == 0 {
		msg := "no eligible node: no nodes match the constraints"
		if matching > 0 {
			msg = "no eligible node: replicas need distinct nodes and all matching nodes already hold one"
		}
		return store.Node{}, msg, false
	}

	// Adoption: a container for this exact slot already lives somewhere
	// eligible — keep it there instead of creating a twin elsewhere.
	for _, o := range obs {
		if o.ordinal != ordinal {
			continue
		}
		for _, n := range eligible {
			if n.ID == o.row.NodeID {
				return n, "", true
			}
		}
	}

	best := eligible[0]
	for _, n := range eligible[1:] {
		bestOnline, nOnline := best.Status == "online", n.Status == "online"
		switch {
		case nOnline != bestOnline:
			if nOnline {
				best = n
			}
		case slotCount[n.ID] != slotCount[best.ID]:
			if slotCount[n.ID] < slotCount[best.ID] {
				best = n
			}
		case n.Name < best.Name:
			best = n
		}
	}
	return best, "", true
}

func constraintsMatch(c *Constraints, n store.Node) bool {
	if len(c.NodeIDs) > 0 {
		found := false
		for _, id := range c.NodeIDs {
			if id == n.ID {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(c.NodeRoles) > 0 {
		for _, want := range c.NodeRoles {
			for _, have := range n.Roles {
				if want == have {
					return true
				}
			}
		}
		return false
	}
	return true
}

// convergeSlot drives one placed slot to the desired state by comparing the
// observed container (matched by ownership labels) against the freshly
// materialized spec.
func (r *Reconciler) convergeSlot(ctx context.Context, w *workload, a store.WorkloadAssignment, obs []observedContainer, nodes map[uuid.UUID]store.Node) {
	if a.NodeID == nil {
		return // unschedulable; plan surfaced it
	}
	if a.NextAttemptAt != nil && time.Now().Before(*a.NextAttemptAt) {
		return // backing off; the tick retries
	}
	node, ok := nodes[*a.NodeID]
	if !ok {
		return // node row vanished; ON DELETE SET NULL frees the slot next pass
	}
	if node.Status != "online" {
		// Blocked ≠ failed: surface once, converge the moment it returns.
		msg := "node offline: " + node.Name
		if deref(a.LastError) != msg {
			if err := r.st.MarkAssignmentBlocked(ctx, store.MarkAssignmentBlockedParams{
				WorkloadID: w.ID, Ordinal: a.Ordinal, LastError: &msg,
			}); err != nil {
				slog.WarnContext(ctx, "reconcile: mark blocked", "workload", w.Name, "err", err)
			}
		}
		return
	}

	desired := materialize(w, a.Ordinal, r.endpoint)
	wantHash := desired.Labels[engine.LabelConfigHash]
	cur := matchSlot(obs, a)
	handle := r.handleFor(node)

	fail := func(step string, err error) {
		if a.LastError == nil {
			slog.WarnContext(ctx, "reconcile: assignment failing",
				"workload", w.Name, "ordinal", a.Ordinal, "node", node.Name, "step", step, "err", err)
		}
		msg := step + ": " + err.Error()
		next := backoffAfter(a.Retries)
		if serr := r.st.MarkAssignmentFailed(ctx, store.MarkAssignmentFailedParams{
			WorkloadID: w.ID, Ordinal: a.Ordinal, LastError: &msg, NextAttemptAt: &next,
		}); serr != nil {
			slog.WarnContext(ctx, "reconcile: record assignment error", "workload", w.Name, "err", serr)
		}
	}
	converged := func(phase string, containerID *string) {
		if a.LastError != nil {
			slog.InfoContext(ctx, "reconcile: assignment recovered",
				"workload", w.Name, "ordinal", a.Ordinal, "node", node.Name)
		}
		if a.Phase == phase && a.Generation == w.Generation && a.LastError == nil &&
			deref(containerID) == deref(a.ContainerID) {
			return // already settled; keep the row quiet
		}
		if err := r.st.MarkAssignmentConverged(ctx, store.MarkAssignmentConvergedParams{
			WorkloadID: w.ID, Ordinal: a.Ordinal, Phase: phase,
			ContainerID: containerID, Generation: w.Generation,
		}); err != nil {
			slog.WarnContext(ctx, "reconcile: record convergence", "workload", w.Name, "err", err)
		}
	}
	deploy := func() {
		r.setPhase(ctx, w, a, PhasePulling)
		pctx, cancel := context.WithTimeout(ctx, imageResolveTimeout)
		defer cancel()
		if _, err := handle.PullImage(pctx, desired.Image); err != nil {
			fail("pull", err)
			return
		}
		r.setPhase(ctx, w, a, PhaseDeploying)
		dctx, cancel2 := context.WithTimeout(ctx, deployTimeout)
		defer cancel2()
		ctr, err := handle.CreateContainer(dctx, desired, engine.PullIfMissing, w.DesiredState == DesiredRunning)
		if err != nil {
			fail("deploy", err)
			return
		}
		r.record(ctx, node.ID, ctr)
		if w.DesiredState == DesiredRunning {
			converged(PhaseReady, &ctr.ID)
		} else {
			converged(PhaseStopped, &ctr.ID)
		}
	}

	switch {
	case cur == nil:
		if w.DesiredState == DesiredStopped {
			converged(PhaseStopped, nil) // absent stays absent; no pre-create
			return
		}
		deploy()

	case cur.hash != wantHash:
		// Replace: remove the old, then the normal deploy path.
		r.setPhase(ctx, w, a, PhaseDeploying)
		if err := handle.RemoveContainer(ctx, cur.row.ContainerID, true); err != nil && !errors.Is(err, cluster.ErrContainerNotFound) {
			fail("replace", err)
			return
		}
		r.dropObserved(ctx, cur)
		deploy()

	case w.DesiredState == DesiredRunning:
		switch cur.row.State {
		case "running", "restarting", "paused":
			// restarting/crash-looping is docker's node-local micro-goal;
			// observed state carries the restart count.
			converged(PhaseReady, &cur.row.ContainerID)
		case "removing":
			return // transient; the next pass sees the outcome
		case "dead":
			r.setPhase(ctx, w, a, PhaseDeploying)
			if err := handle.RemoveContainer(ctx, cur.row.ContainerID, true); err != nil && !errors.Is(err, cluster.ErrContainerNotFound) {
				fail("replace dead", err)
				return
			}
			r.dropObserved(ctx, cur)
			deploy()
		default: // created, exited
			r.setPhase(ctx, w, a, PhaseDeploying)
			ctr, err := handle.StartContainer(ctx, cur.row.ContainerID)
			if err != nil {
				fail("start", err)
				return
			}
			r.record(ctx, node.ID, ctr)
			converged(PhaseReady, &ctr.ID)
		}

	default: // desired stopped
		switch cur.row.State {
		case "running", "restarting", "paused":
			r.setPhase(ctx, w, a, PhaseStopping)
			ctr, err := handle.StopContainer(ctx, cur.row.ContainerID, 0)
			if err != nil {
				fail("stop", err)
				return
			}
			r.record(ctx, node.ID, ctr)
			converged(PhaseStopped, &ctr.ID)
		case "removing":
			return
		default:
			converged(PhaseStopped, &cur.row.ContainerID)
		}
	}
}

// teardown converges a deleting workload to absence and finally drops its
// rows. Offline nodes block completion visibly (the slot stays, marked) —
// deleting the node is the explicit escape hatch, and the orphan GC catches
// anything that resurfaces later.
func (r *Reconciler) teardown(ctx context.Context, w *workload, obs []observedContainer, nodes map[uuid.UUID]store.Node) {
	asgs, err := r.st.ListAssignmentsForWorkload(ctx, w.ID)
	if err != nil {
		slog.WarnContext(ctx, "reconcile: list workload assignments", "workload", w.Name, "err", err)
		return
	}
	remaining := 0
	for _, a := range asgs {
		if !r.removeSlot(ctx, w, a, obs, nodes) {
			remaining++
		}
	}
	if remaining > 0 {
		return
	}
	if _, err := r.st.DeleteWorkload(ctx, w.ID); err != nil {
		slog.WarnContext(ctx, "reconcile: delete workload row", "workload", w.Name, "err", err)
		return
	}
	slog.InfoContext(ctx, "reconcile: workload deleted", "workload", w.Name)
}

// removeSlot converges one slot to absence; reports whether its row is gone.
func (r *Reconciler) removeSlot(ctx context.Context, w *workload, a store.WorkloadAssignment, obs []observedContainer, nodes map[uuid.UUID]store.Node) bool {
	if cur := matchSlot(obs, a); cur != nil {
		node, ok := nodes[cur.row.NodeID]
		if !ok {
			// Node rows cascade-delete their observations; a miss here means
			// the snapshot raced a node removal. Treat as gone.
			ok = false
		}
		if !ok || node.Status != "online" {
			msg := "node offline; container removal pending"
			if a.Phase != PhaseRemoving || deref(a.LastError) != msg {
				r.setPhase(ctx, w, a, PhaseRemoving)
				if err := r.st.MarkAssignmentBlocked(ctx, store.MarkAssignmentBlockedParams{
					WorkloadID: w.ID, Ordinal: a.Ordinal, LastError: &msg,
				}); err != nil {
					slog.WarnContext(ctx, "reconcile: mark blocked", "workload", w.Name, "err", err)
				}
			}
			return false
		}
		if a.Phase != PhaseRemoving {
			r.setPhase(ctx, w, a, PhaseRemoving)
		}
		if err := r.handleFor(node).RemoveContainer(ctx, cur.row.ContainerID, true); err != nil && !errors.Is(err, cluster.ErrContainerNotFound) {
			if a.LastError == nil {
				slog.WarnContext(ctx, "reconcile: slot removal failing",
					"workload", w.Name, "ordinal", a.Ordinal, "node", node.Name, "err", err)
			}
			msg := "remove: " + err.Error()
			next := backoffAfter(a.Retries)
			if serr := r.st.MarkAssignmentFailed(ctx, store.MarkAssignmentFailedParams{
				WorkloadID: w.ID, Ordinal: a.Ordinal, LastError: &msg, NextAttemptAt: &next,
			}); serr != nil {
				slog.WarnContext(ctx, "reconcile: record assignment error", "workload", w.Name, "err", serr)
			}
			return false
		}
		r.dropObserved(ctx, cur)
	}
	if _, err := r.st.DeleteWorkloadAssignment(ctx, store.DeleteWorkloadAssignmentParams{
		WorkloadID: w.ID, Ordinal: a.Ordinal,
	}); err != nil {
		slog.WarnContext(ctx, "reconcile: delete assignment row", "workload", w.Name, "err", err)
		return false
	}
	return true
}

// matchSlot finds the observed container backing an assignment — strictly by
// ownership labels on the assigned node. A same-named container without our
// labels is foreign and surfaces as a create conflict, never gets adopted or
// removed.
func matchSlot(obs []observedContainer, a store.WorkloadAssignment) *observedContainer {
	if a.NodeID == nil {
		return nil
	}
	for i := range obs {
		if obs[i].ordinal == a.Ordinal && obs[i].row.NodeID == *a.NodeID {
			return &obs[i]
		}
	}
	return nil
}

func (r *Reconciler) setPhase(ctx context.Context, w *workload, a store.WorkloadAssignment, phase string) {
	if a.Phase == phase {
		return
	}
	if err := r.st.SetAssignmentPhase(ctx, store.SetAssignmentPhaseParams{
		WorkloadID: w.ID, Ordinal: a.Ordinal, Phase: phase,
	}); err != nil {
		slog.WarnContext(ctx, "reconcile: set phase", "workload", w.Name, "phase", phase, "err", err)
	}
}

// record writes a mutation's post-op state through to observed state, the
// same doctrine as every master-side mutation: instant visibility, heartbeat
// stays the reconciler of record. It also prevents the self-race where the
// next pass would not yet see a just-created container.
func (r *Reconciler) record(ctx context.Context, nodeID uuid.UUID, ctr engine.Container) {
	if err := cluster.RecordObservedContainer(ctx, r.st, nodeID, ctr); err != nil {
		slog.WarnContext(ctx, "reconcile: write through observed state", "container", ctr.Name, "err", err)
	}
}

func (r *Reconciler) dropObserved(ctx context.Context, cur *observedContainer) {
	if _, err := r.st.DeleteNodeContainer(ctx, store.DeleteNodeContainerParams{
		NodeID: cur.row.NodeID, ContainerID: cur.row.ContainerID,
	}); err != nil {
		slog.WarnContext(ctx, "reconcile: drop observed row", "container", cur.row.Name, "err", err)
	}
}
