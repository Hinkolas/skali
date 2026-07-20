package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/manifest"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/store"
)

// projectsHandlers is the definition-plane surface: projects and their draft
// documents. Every route sits behind RequireAuth; members have full project
// access.
type projectsHandlers struct {
	projects *project.Service
}

// pathID parses the {id} route param, writing a 404 on malformed ids so they
// are indistinguishable from missing resources.
func pathID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return uuid.UUID{}, false
	}
	return id, true
}

// --- payloads (shapes mirror api/openapi.yaml exactly) ---

type projectPayload struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	DisplayName string    `json:"display_name"`
	SourceMode  string    `json:"source_mode"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func newProjectPayload(p *store.Project) projectPayload {
	return projectPayload{
		ID:          p.ID.String(),
		Name:        p.Name,
		DisplayName: p.DisplayName,
		SourceMode:  p.SourceMode,
		CreatedAt:   p.CreatedAt,
		UpdatedAt:   p.UpdatedAt,
	}
}

type draftPayload struct {
	Version    int64                      `json:"version"`
	Format     string                     `json:"format"`
	Source     string                     `json:"source"`
	Hash       string                     `json:"hash"`
	Definition compiler.ProjectDefinition `json:"definition"`
}

func newDraftPayload(d *project.Draft) draftPayload {
	return draftPayload{
		Version:    d.Version,
		Format:     d.Format,
		Source:     string(d.Source),
		Hash:       d.Hash,
		Definition: d.Definition,
	}
}

type diagnosticPayload struct {
	File    string `json:"file"`
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Message string `json:"message"`
}

// writeManifestDiagnostics reports an invalid manifest as 422 with the full
// diagnostic list next to the standard error envelope.
func writeManifestDiagnostics(w http.ResponseWriter, diagnostics manifest.Diagnostics) {
	payload := make([]diagnosticPayload, len(diagnostics))
	for i, d := range diagnostics {
		payload[i] = diagnosticPayload{
			File:    d.File,
			Path:    d.Path,
			Line:    d.Line,
			Column:  d.Column,
			Message: d.Message,
		}
	}
	writeJSON(w, http.StatusUnprocessableEntity, struct {
		Error       errorDetail         `json:"error"`
		Diagnostics []diagnosticPayload `json:"diagnostics"`
	}{
		Error:       errorDetail{Code: codeInvalidManifest, Message: "the manifest is invalid"},
		Diagnostics: payload,
	})
}

// writeProjectError maps project sentinel errors onto the envelope; anything
// unrecognized is logged and reported as an opaque 500.
func writeProjectError(ctx context.Context, w http.ResponseWriter, err error) {
	var diagnostics manifest.Diagnostics
	switch {
	case errors.As(err, &diagnostics):
		writeManifestDiagnostics(w, diagnostics)
	case errors.Is(err, project.ErrProjectNotFound),
		errors.Is(err, project.ErrEnvironmentNotFound),
		errors.Is(err, project.ErrDraftNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
	case errors.Is(err, project.ErrProjectNameTaken):
		writeError(w, http.StatusConflict, codeConflict, "a project with this name already exists")
	case errors.Is(err, project.ErrEnvironmentNameTaken):
		writeError(w, http.StatusConflict, codeConflict, "an environment with this name already exists")
	case errors.Is(err, project.ErrVersionConflict):
		writeError(w, http.StatusConflict, codeVersionConflict,
			"draft version conflict: fetch the draft and retry with its current version")
	case errors.Is(err, project.ErrNameMismatch):
		writeError(w, http.StatusUnprocessableEntity, codeInvalidManifest,
			"manifest name does not match the project name")
	case errors.Is(err, project.ErrInvalidName),
		errors.Is(err, project.ErrInvalidSourceMode),
		errors.Is(err, project.ErrInvalidFormat):
		writeError(w, http.StatusBadRequest, codeBadRequest, strings.TrimPrefix(err.Error(), "project: "))
	default:
		writeInternalError(ctx, w, "project error", err)
	}
}

// POST /v1/projects
func (h *projectsHandlers) create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, codeBadRequest, "name is required")
		return
	}
	proj, err := h.projects.Create(r.Context(), req.Name, req.DisplayName)
	if err != nil {
		writeProjectError(r.Context(), w, err)
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		Project projectPayload `json:"project"`
	}{newProjectPayload(proj)})
}

// GET /v1/projects
func (h *projectsHandlers) list(w http.ResponseWriter, r *http.Request) {
	projects, err := h.projects.List(r.Context())
	if err != nil {
		writeProjectError(r.Context(), w, err)
		return
	}
	payload := make([]projectPayload, len(projects))
	for i := range projects {
		payload[i] = newProjectPayload(&projects[i])
	}
	writeJSON(w, http.StatusOK, struct {
		Projects []projectPayload `json:"projects"`
	}{payload})
}

// GET /v1/projects/{id}
func (h *projectsHandlers) get(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	proj, err := h.projects.Get(r.Context(), id)
	if err != nil {
		writeProjectError(r.Context(), w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Project projectPayload `json:"project"`
	}{newProjectPayload(proj)})
}

// PATCH /v1/projects/{id}
func (h *projectsHandlers) update(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		DisplayName *string `json:"display_name"`
		SourceMode  *string `json:"source_mode"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	if req.DisplayName == nil && req.SourceMode == nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, "nothing to update: provide display_name and/or source_mode")
		return
	}
	proj, err := h.projects.Update(r.Context(), id, req.DisplayName, req.SourceMode)
	if err != nil {
		writeProjectError(r.Context(), w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Project projectPayload `json:"project"`
	}{newProjectPayload(proj)})
}

// DELETE /v1/projects/{id}
func (h *projectsHandlers) delete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := h.projects.Delete(r.Context(), id); err != nil {
		writeProjectError(r.Context(), w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /v1/projects/{id}/draft
func (h *projectsHandlers) getDraft(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	draft, err := h.projects.GetDraft(r.Context(), id)
	if err != nil {
		writeProjectError(r.Context(), w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Draft draftPayload `json:"draft"`
	}{newDraftPayload(draft)})
}

// PUT /v1/projects/{id}/draft
func (h *projectsHandlers) putDraft(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Source          string `json:"source"`
		Format          string `json:"format"`
		ExpectedVersion int64  `json:"expected_version"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	if req.Source == "" {
		writeError(w, http.StatusBadRequest, codeBadRequest, "source is required")
		return
	}
	if req.Format == "" {
		req.Format = "yaml"
	}
	draft, err := h.projects.SubmitDraft(r.Context(), id, project.DraftSubmission{
		Source:          []byte(req.Source),
		Format:          req.Format,
		ExpectedVersion: req.ExpectedVersion,
	})
	if err != nil {
		writeProjectError(r.Context(), w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Draft draftPayload `json:"draft"`
	}{newDraftPayload(draft)})
}
