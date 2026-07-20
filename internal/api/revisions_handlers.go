package api

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/deploy"
	"github.com/Hinkolas/skali/internal/revision"
	"github.com/Hinkolas/skali/internal/store"
)

// revisionsHandlers is the read surface over immutable revisions and the
// environment target pointer, plus rollback (pointing the target at an
// existing revision).
type revisionsHandlers struct {
	deploy *deploy.Service
}

type revisionSummaryPayload struct {
	ID              string    `json:"id"`
	ProjectID       string    `json:"project_id"`
	EnvironmentID   string    `json:"environment_id"`
	SchemaVersion   string    `json:"schema_version"`
	Checksum        string    `json:"checksum"`
	DefinitionHash  string    `json:"definition_hash"`
	ValuesHash      string    `json:"values_hash"`
	CompilerVersion string    `json:"compiler_version"`
	CreatedAt       time.Time `json:"created_at"`
}

func newRevisionSummaryPayload(r *store.ListRevisionsRow) revisionSummaryPayload {
	return revisionSummaryPayload{
		ID:              r.ID.String(),
		ProjectID:       r.ProjectID.String(),
		EnvironmentID:   r.EnvironmentID.String(),
		SchemaVersion:   r.SchemaVersion,
		Checksum:        r.Checksum,
		DefinitionHash:  r.DefinitionHash,
		ValuesHash:      r.ValuesHash,
		CompilerVersion: r.CompilerVersion,
		CreatedAt:       r.CreatedAt,
	}
}

type targetPayload struct {
	TargetRevisionID *string   `json:"target_revision_id"`
	ActiveRevisionID *string   `json:"active_revision_id"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func newTargetPayload(t *store.EnvironmentTarget) targetPayload {
	payload := targetPayload{UpdatedAt: t.UpdatedAt}
	if t.TargetRevisionID != nil {
		id := t.TargetRevisionID.String()
		payload.TargetRevisionID = &id
	}
	if t.ActiveRevisionID != nil {
		id := t.ActiveRevisionID.String()
		payload.ActiveRevisionID = &id
	}
	return payload
}

// GET /v1/environments/{id}/revisions
func (h *revisionsHandlers) list(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	// Distinguish an unknown environment from an environment without
	// revisions: the target row exists for every environment.
	if _, err := h.deploy.Target(r.Context(), id); err != nil {
		writeDeployError(r.Context(), w, err)
		return
	}
	rows, err := h.deploy.ListRevisions(r.Context(), id)
	if err != nil {
		writeDeployError(r.Context(), w, err)
		return
	}
	payload := make([]revisionSummaryPayload, len(rows))
	for i := range rows {
		payload[i] = newRevisionSummaryPayload(&rows[i])
	}
	writeJSON(w, http.StatusOK, struct {
		Revisions []revisionSummaryPayload `json:"revisions"`
	}{payload})
}

// GET /v1/revisions/{id}
func (h *revisionsHandlers) get(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	document, err := h.deploy.GetRevision(r.Context(), id)
	if err != nil {
		writeDeployError(r.Context(), w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Revision *revision.Revision `json:"revision"`
	}{document})
}

// GET /v1/environments/{id}/target
func (h *revisionsHandlers) getTarget(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	target, err := h.deploy.Target(r.Context(), id)
	if err != nil {
		writeDeployError(r.Context(), w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Target targetPayload `json:"target"`
	}{newTargetPayload(target)})
}

// PUT /v1/environments/{id}/target
func (h *revisionsHandlers) putTarget(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		RevisionID string `json:"revision_id"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	revisionID, err := uuid.Parse(req.RevisionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, "revision_id must be a UUID")
		return
	}
	if err := h.deploy.Rollback(r.Context(), id, revisionID); err != nil {
		writeDeployError(r.Context(), w, err)
		return
	}
	target, err := h.deploy.Target(r.Context(), id)
	if err != nil {
		writeDeployError(r.Context(), w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Target targetPayload `json:"target"`
	}{newTargetPayload(target)})
}
