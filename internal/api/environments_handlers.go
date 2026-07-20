package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/Hinkolas/skali/internal/deploy"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/store"
)

// environmentsHandlers is the environment surface. Environments hang off a
// project for creation and listing, and are addressed directly otherwise.
type environmentsHandlers struct {
	projects *project.Service
	deploy   *deploy.Service
	journal  *journal.Service
}

type environmentPayload struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

func newEnvironmentPayload(e *store.Environment) environmentPayload {
	return environmentPayload{
		ID:        e.ID.String(),
		ProjectID: e.ProjectID.String(),
		Name:      e.Name,
		CreatedAt: e.CreatedAt,
	}
}

// POST /v1/projects/{id}/environments
func (h *environmentsHandlers) create(w http.ResponseWriter, r *http.Request) {
	projectID, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, codeBadRequest, "name is required")
		return
	}
	env, err := h.projects.CreateEnvironment(r.Context(), projectID, req.Name)
	if err != nil {
		writeProjectError(r.Context(), w, err)
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		Environment environmentPayload `json:"environment"`
	}{newEnvironmentPayload(env)})
}

// GET /v1/projects/{id}/environments
func (h *environmentsHandlers) list(w http.ResponseWriter, r *http.Request) {
	projectID, ok := pathID(w, r)
	if !ok {
		return
	}
	environments, err := h.projects.ListEnvironments(r.Context(), projectID)
	if err != nil {
		writeProjectError(r.Context(), w, err)
		return
	}
	payload := make([]environmentPayload, len(environments))
	for i := range environments {
		payload[i] = newEnvironmentPayload(&environments[i])
	}
	writeJSON(w, http.StatusOK, struct {
		Environments []environmentPayload `json:"environments"`
	}{payload})
}

// GET /v1/environments/{id}
func (h *environmentsHandlers) get(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	env, err := h.projects.GetEnvironment(r.Context(), id)
	if err != nil {
		writeProjectError(r.Context(), w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Environment environmentPayload `json:"environment"`
	}{newEnvironmentPayload(env)})
}

// POST /v1/environments/{id}/teardown
func (h *environmentsHandlers) teardown(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Purge bool `json:"purge"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSON(w, r, &req); err != nil {
			writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
			return
		}
	}
	user := UserFrom(r.Context())
	run, err := h.deploy.Teardown(r.Context(), id, req.Purge, h.journal, user.ID.String())
	if err != nil {
		switch {
		case errors.Is(err, deploy.ErrEnvironmentNotFound):
			writeError(w, http.StatusNotFound, codeNotFound, "environment not found")
		case errors.Is(err, deploy.ErrEnvironmentReleasing):
			writeError(w, http.StatusConflict, codeConflict, "environment is already releasing")
		case errors.Is(err, deploy.ErrDeploymentInFlight):
			writeError(w, http.StatusConflict, codeDeploymentInFlight,
				"a deployment is in flight; cancel its run or wait for it to finish")
		default:
			writeInternalError(r.Context(), w, "teardown environment", err)
		}
		return
	}
	writeJSON(w, http.StatusAccepted, struct {
		RunID string `json:"run_id"`
		Purge bool   `json:"purge"`
	}{run.ID.String(), req.Purge})
}

// DELETE /v1/environments/{id}. The raw delete removes rows only and leaves
// any cluster state orphaned; POST {id}/teardown is the paved path.
func (h *environmentsHandlers) delete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := h.projects.DeleteEnvironment(r.Context(), id); err != nil {
		writeProjectError(r.Context(), w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
