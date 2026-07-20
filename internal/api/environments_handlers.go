package api

import (
	"net/http"
	"time"

	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/store"
)

// environmentsHandlers is the environment surface. Environments hang off a
// project for creation and listing, and are addressed directly otherwise.
type environmentsHandlers struct {
	projects *project.Service
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

// DELETE /v1/environments/{id}
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
