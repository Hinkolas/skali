package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/reconcile"
	"github.com/Hinkolas/skali/internal/store"
)

// workloadsHandlers is the desired-state surface: declare what should run,
// the reconciler makes it so. Mutations return fast — convergence progress
// is read back through the embedded assignments (phases, errors, backoff),
// never awaited in a request. Reads are store-only so they keep serving when
// the registry gate is closed; writes need the service (nil = 503, workloads
// require the mirror).
type workloadsHandlers struct {
	st        *store.Store
	workloads *reconcile.Service // nil = registry (and thus workloads) disabled
}

type workloadPayload struct {
	ID              uuid.UUID             `json:"id"`
	Name            string                `json:"name"`
	Kind            string                `json:"kind"`
	DesiredState    string                `json:"desired_state"`
	Replicas        int32                 `json:"replicas"`
	Constraints     reconcile.Constraints `json:"constraints"`
	Image           string                `json:"image"`
	Spec            reconcile.Spec        `json:"spec"`
	Generation      int64                 `json:"generation"`
	Status          string                `json:"status"`
	ImageRepository *string               `json:"image_repository,omitempty"`
	ImageDigest     *string               `json:"image_digest,omitempty"`
	LastError       *string               `json:"last_error,omitempty"`
	NextAttemptAt   *time.Time            `json:"next_attempt_at,omitempty"`
	CreatedAt       time.Time             `json:"created_at"`
	UpdatedAt       time.Time             `json:"updated_at"`
	Assignments     []assignmentPayload   `json:"assignments"`
}

type assignmentPayload struct {
	Ordinal       int32      `json:"ordinal"`
	NodeID        *uuid.UUID `json:"node_id,omitempty"`
	NodeName      string     `json:"node_name,omitempty"`
	Phase         string     `json:"phase"`
	ContainerName string     `json:"container_name"`
	ContainerID   *string    `json:"container_id,omitempty"`
	Generation    int64      `json:"generation"`
	Retries       int32      `json:"retries"`
	NextAttemptAt *time.Time `json:"next_attempt_at,omitempty"`
	LastError     *string    `json:"last_error,omitempty"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

func newWorkloadPayload(w store.Workload, asgs []store.ListWorkloadAssignmentsWithNodesRow) workloadPayload {
	p := workloadPayload{
		ID: w.ID, Name: w.Name, Kind: w.Kind, DesiredState: w.DesiredState,
		Replicas: w.Replicas, Image: w.Image, Generation: w.Generation,
		ImageRepository: w.ResolvedRepository, ImageDigest: w.ResolvedDigest,
		LastError: w.LastError, NextAttemptAt: w.NextAttemptAt,
		CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt,
		Assignments: make([]assignmentPayload, 0, len(asgs)),
	}
	// Best-effort decoding: the service validated these on the way in.
	_ = json.Unmarshal(w.Constraints, &p.Constraints)
	_ = json.Unmarshal(w.Spec, &p.Spec)
	for _, row := range asgs {
		a := row.WorkloadAssignment
		p.Assignments = append(p.Assignments, assignmentPayload{
			Ordinal: a.Ordinal, NodeID: a.NodeID, NodeName: row.NodeName,
			Phase: a.Phase, ContainerName: a.ContainerName, ContainerID: a.ContainerID,
			Generation: a.Generation, Retries: a.Retries,
			NextAttemptAt: a.NextAttemptAt, LastError: a.LastError, UpdatedAt: a.UpdatedAt,
		})
	}
	p.Status = rollupStatus(w, asgs)
	return p
}

// rollupStatus computes the at-a-glance state, never stored: deleting >
// degraded (anything erroring or unplaceable) > importing (no digest pin
// yet) > converging (work in flight or generation lag) > running|stopped.
func rollupStatus(w store.Workload, asgs []store.ListWorkloadAssignmentsWithNodesRow) string {
	if w.DesiredState == reconcile.DesiredDeleting {
		return "deleting"
	}
	settled := 0
	for _, row := range asgs {
		a := row.WorkloadAssignment
		if a.Phase == reconcile.PhaseUnschedulable || a.LastError != nil {
			return "degraded"
		}
		if (a.Phase == reconcile.PhaseReady || a.Phase == reconcile.PhaseStopped) && a.Generation == w.Generation {
			settled++
		}
	}
	if w.LastError != nil {
		return "degraded"
	}
	if w.ResolvedDigest == nil || *w.ResolvedDigest == "" {
		return "importing"
	}
	if settled != len(asgs) || len(asgs) != int(w.Replicas) {
		return "converging"
	}
	if w.DesiredState == reconcile.DesiredStopped {
		return "stopped"
	}
	return "running"
}

// enabled gates writes: workloads need the mirror (unified image handling),
// so they share the registry's CLUSTER_ADDR gate.
func (h *workloadsHandlers) enabled(w http.ResponseWriter) bool {
	if h.workloads == nil {
		writeError(w, http.StatusServiceUnavailable, codeRegistryDisabled,
			"workloads are disabled: they need the image mirror — set CLUSTER_ADDR on the master")
		return false
	}
	return true
}

func (h *workloadsHandlers) assignmentsByWorkload(ctx context.Context) (map[uuid.UUID][]store.ListWorkloadAssignmentsWithNodesRow, error) {
	rows, err := h.st.ListWorkloadAssignmentsWithNodes(ctx)
	if err != nil {
		return nil, err
	}
	byWorkload := make(map[uuid.UUID][]store.ListWorkloadAssignmentsWithNodesRow)
	for _, row := range rows {
		byWorkload[row.WorkloadAssignment.WorkloadID] = append(byWorkload[row.WorkloadAssignment.WorkloadID], row)
	}
	return byWorkload, nil
}

func (h *workloadsHandlers) list(w http.ResponseWriter, r *http.Request) {
	rows, err := h.st.ListWorkloads(r.Context())
	if err != nil {
		writeInternalError(r.Context(), w, "list workloads", err)
		return
	}
	byWorkload, err := h.assignmentsByWorkload(r.Context())
	if err != nil {
		writeInternalError(r.Context(), w, "list assignments", err)
		return
	}
	payloads := make([]workloadPayload, 0, len(rows))
	for _, row := range rows {
		payloads = append(payloads, newWorkloadPayload(row, byWorkload[row.ID]))
	}
	writeJSON(w, http.StatusOK, struct {
		Workloads []workloadPayload `json:"workloads"`
	}{payloads})
}

func (h *workloadsHandlers) get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return
	}
	row, err := h.st.GetWorkloadByID(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return
	}
	if err != nil {
		writeInternalError(r.Context(), w, "get workload", err)
		return
	}
	h.respond(w, r, http.StatusOK, row)
}

// workloadCreateRequest declares a workload. spec and constraints are
// objects (replaced as units on PATCH), matching the service surface.
type workloadCreateRequest struct {
	Name         string                 `json:"name"`
	Kind         string                 `json:"kind"`
	Image        string                 `json:"image"`
	Replicas     *int                   `json:"replicas"`
	DesiredState string                 `json:"desired_state"`
	Constraints  *reconcile.Constraints `json:"constraints"`
	Spec         *reconcile.Spec        `json:"spec"`
}

func (h *workloadsHandlers) create(w http.ResponseWriter, r *http.Request) {
	if !h.enabled(w) {
		return
	}
	var req workloadCreateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	in := reconcile.WorkloadInput{
		Name: req.Name, Kind: req.Kind, Image: req.Image,
		Replicas: 1, DesiredState: req.DesiredState,
	}
	if req.Replicas != nil {
		in.Replicas = *req.Replicas
	}
	if req.Constraints != nil {
		in.Constraints = *req.Constraints
	}
	if req.Spec != nil {
		in.Spec = *req.Spec
	}
	row, err := h.workloads.Create(r.Context(), in)
	if err != nil {
		writeWorkloadError(r.Context(), w, err)
		return
	}
	slog.InfoContext(r.Context(), "api: workload created",
		"workload", row.Name, "replicas", row.Replicas, "by", UserFrom(r.Context()).ID)
	h.respond(w, r, http.StatusCreated, row)
}

type workloadUpdateRequest struct {
	Image        *string                `json:"image"`
	Replicas     *int                   `json:"replicas"`
	DesiredState *string                `json:"desired_state"`
	Constraints  *reconcile.Constraints `json:"constraints"`
	Spec         *reconcile.Spec        `json:"spec"`
}

func (h *workloadsHandlers) update(w http.ResponseWriter, r *http.Request) {
	if !h.enabled(w) {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return
	}
	var req workloadUpdateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	row, err := h.workloads.Update(r.Context(), id, reconcile.WorkloadUpdate{
		Image: req.Image, Replicas: req.Replicas, DesiredState: req.DesiredState,
		Constraints: req.Constraints, Spec: req.Spec,
	})
	if err != nil {
		writeWorkloadError(r.Context(), w, err)
		return
	}
	slog.InfoContext(r.Context(), "api: workload updated",
		"workload", row.Name, "generation", row.Generation, "by", UserFrom(r.Context()).ID)
	h.respond(w, r, http.StatusOK, row)
}

func (h *workloadsHandlers) delete(w http.ResponseWriter, r *http.Request) {
	if !h.enabled(w) {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return
	}
	if err := h.workloads.Delete(r.Context(), id); err != nil {
		writeWorkloadError(r.Context(), w, err)
		return
	}
	slog.InfoContext(r.Context(), "api: workload deletion started",
		"id", id, "by", UserFrom(r.Context()).ID)
	// 202: the reconciler converges to absence, then the rows disappear.
	w.WriteHeader(http.StatusAccepted)
}

func (h *workloadsHandlers) respond(w http.ResponseWriter, r *http.Request, status int, row store.Workload) {
	byWorkload, err := h.assignmentsByWorkload(r.Context())
	if err != nil {
		writeInternalError(r.Context(), w, "list assignments", err)
		return
	}
	writeJSON(w, status, struct {
		Workload workloadPayload `json:"workload"`
	}{newWorkloadPayload(row, byWorkload[row.ID])})
}

// writeWorkloadError maps reconcile sentinels onto the envelope.
func writeWorkloadError(ctx context.Context, w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, reconcile.ErrInvalidWorkload):
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
	case errors.Is(err, reconcile.ErrWorkloadNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
	case errors.Is(err, reconcile.ErrNameTaken):
		writeError(w, http.StatusConflict, codeConflict, err.Error())
	case errors.Is(err, reconcile.ErrWorkloadDeleting):
		writeError(w, http.StatusConflict, codeConflict, "the workload is being deleted")
	default:
		writeInternalError(ctx, w, "workload error", err)
	}
}
