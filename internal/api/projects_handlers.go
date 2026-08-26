package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/authz"
	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/reconcile"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/yamldoc"
)

// projectsHandlers is the definition-plane surface: projects and their draft
// documents. The scope middleware (access.go) has already answered 404 for
// invisible projects and 403 for insufficient roles; handlers read the grant
// back for payloads and for the checks only they can make (project creation
// needs the create_projects permission, the list filters to memberships).
// The reconcile kernel is only consulted for the optional list summary
// rollup.
type projectsHandlers struct {
	projects  *project.Service
	reconcile *reconcile.Kernel
	resolver  *authz.Resolver
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
	ID          string                 `json:"id"`
	Name        string                 `json:"name"`
	DisplayName string                 `json:"display_name"`
	SourceMode  string                 `json:"source_mode"`
	CreatedAt   time.Time              `json:"created_at"`
	UpdatedAt   time.Time              `json:"updated_at"`
	Access      projectAccessPayload   `json:"access"`
	Summary     *projectSummaryPayload `json:"summary,omitempty"`
}

// projectAccessPayload is the caller's standing: the project role and the
// effective role per environment (locked ones report none).
type projectAccessPayload struct {
	Role         string            `json:"role"`
	Environments map[string]string `json:"environments"`
}

func newProjectAccessPayload(grant *authz.Grant) projectAccessPayload {
	return projectAccessPayload{Role: grant.ProjectRole.String(), Environments: grant.Roles()}
}

type projectSummaryPayload struct {
	Environments  []summaryEnvironmentPayload `json:"environments"`
	ServiceCounts serviceCountsPayload        `json:"service_counts"`
}

// summaryEnvironmentPayload: a locked environment carries id, name, and
// access only; state and health are part of its contents.
type summaryEnvironmentPayload struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Access string `json:"access"`
	State  string `json:"state,omitempty"`
	Health string `json:"health,omitempty"`
}

type serviceCountsPayload struct {
	Applications int `json:"applications"`
	Databases    int `json:"databases"`
	Buckets      int `json:"buckets"`
}

func newProjectPayload(p *store.Project, grant *authz.Grant) projectPayload {
	return projectPayload{
		ID:          p.ID.String(),
		Name:        p.Name,
		DisplayName: p.DisplayName,
		SourceMode:  p.SourceMode,
		CreatedAt:   p.CreatedAt,
		UpdatedAt:   p.UpdatedAt,
		Access:      newProjectAccessPayload(grant),
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
func writeManifestDiagnostics(w http.ResponseWriter, diagnostics yamldoc.Diagnostics) {
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
	var diagnostics yamldoc.Diagnostics
	switch {
	case errors.As(err, &diagnostics):
		writeManifestDiagnostics(w, diagnostics)
	case errors.Is(err, project.ErrProjectNotFound),
		errors.Is(err, project.ErrEnvironmentNotFound),
		errors.Is(err, project.ErrDraftNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
	case errors.Is(err, project.ErrProjectNameTaken):
		writeError(w, http.StatusConflict, codeConflict, "a project with this name already exists")
	case errors.Is(err, project.ErrProjectHasEnvironments):
		writeError(w, http.StatusConflict, codeConflict,
			"the project still has environments; remove each one first (skali env rm)")
	case errors.Is(err, project.ErrEnvironmentNameTaken):
		writeError(w, http.StatusConflict, codeConflict, "an environment with this name already exists")
	case errors.Is(err, project.ErrVersionConflict):
		writeError(w, http.StatusConflict, codeVersionConflict,
			"draft version conflict: fetch the draft and retry with its current version")
	case errors.Is(err, project.ErrNameMismatch):
		writeError(w, http.StatusUnprocessableEntity, codeInvalidManifest,
			"manifest name does not match the project name")
	case errors.Is(err, project.ErrUserNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "user not found")
	case errors.Is(err, project.ErrNotMember):
		writeError(w, http.StatusConflict, codeConflict, "user is not a member of the project")
	case errors.Is(err, project.ErrInvalidName),
		errors.Is(err, project.ErrInvalidSourceMode),
		errors.Is(err, project.ErrInvalidFormat),
		errors.Is(err, project.ErrInvalidRole),
		errors.Is(err, project.ErrInvalidSettings):
		writeError(w, http.StatusBadRequest, codeBadRequest, strings.TrimPrefix(err.Error(), "project: "))
	default:
		writeInternalError(ctx, w, "project error", err)
	}
}

// POST /v1/projects: instance admins always, members with create_projects.
// The creator becomes the project's admin.
func (h *projectsHandlers) create(w http.ResponseWriter, r *http.Request) {
	user := UserFrom(r.Context())
	if !h.resolver.MayCreateProject(user) {
		writeError(w, http.StatusForbidden, codeForbidden, "creating projects requires the create_projects permission")
		return
	}
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
	proj, err := h.projects.Create(r.Context(), req.Name, req.DisplayName, user.ID)
	if err != nil {
		writeProjectError(r.Context(), w, err)
		return
	}
	grant, err := h.resolver.Project(r.Context(), user, proj.ID)
	if err != nil {
		writeInternalError(r.Context(), w, "resolve access", err)
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		Project projectPayload `json:"project"`
	}{newProjectPayload(proj, grant)})
}

// GET /v1/projects: the caller's memberships (instance admins: every
// project), each with the caller's access.
func (h *projectsHandlers) list(w http.ResponseWriter, r *http.Request) {
	projects, grants, err := h.resolver.All(r.Context(), UserFrom(r.Context()))
	if err != nil {
		writeInternalError(r.Context(), w, "resolve access", err)
		return
	}
	payload := make([]projectPayload, len(projects))
	for i := range projects {
		payload[i] = newProjectPayload(&projects[i], grants[projects[i].ID])
	}
	if r.URL.Query().Get("include") == "summary" {
		if err := h.attachSummaries(r.Context(), payload, grants); err != nil {
			writeProjectError(r.Context(), w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, struct {
		Projects []projectPayload `json:"projects"`
	}{payload})
}

// healthRank orders service healths for the rollup: anything mixed with
// healthy pulls the badge toward the worse state, and unknown outranks
// healthy so a half-observed environment never reads as fine.
var healthRank = map[module.Health]int{
	module.HealthHealthy:     1,
	module.HealthUnknown:     2,
	module.HealthProgressing: 3,
	module.HealthDegraded:    4,
	module.HealthUnhealthy:   5,
}

// attachSummaries decorates the list payload with environments, states,
// health rollups, and draft service counts. Locked environments keep their
// name and access only.
func (h *projectsHandlers) attachSummaries(ctx context.Context, payload []projectPayload, grants map[uuid.UUID]*authz.Grant) error {
	summaries, err := h.projects.ListSummaries(ctx)
	if err != nil {
		return err
	}
	for i := range payload {
		id, err := uuid.Parse(payload[i].ID)
		if err != nil {
			continue
		}
		summary := summaries[id]
		grant := grants[id]
		entry := &projectSummaryPayload{
			Environments: make([]summaryEnvironmentPayload, 0, len(summary.Environments)),
			ServiceCounts: serviceCountsPayload{
				Applications: summary.ServiceCounts.Applications,
				Databases:    summary.ServiceCounts.Databases,
				Buckets:      summary.ServiceCounts.Buckets,
			},
		}
		for _, env := range summary.Environments {
			item := summaryEnvironmentPayload{ID: env.ID.String(), Name: env.Name, Access: authz.None.String()}
			if grant == nil {
				entry.Environments = append(entry.Environments, item)
				continue
			}
			if envGrant, ok := grant.Environment(env.ID); ok {
				item.Access = envGrant.Role.String()
				if !envGrant.Locked() {
					item.State = env.State
					item.Health = string(h.environmentHealth(ctx, env.ID))
				}
			}
			entry.Environments = append(entry.Environments, item)
		}
		payload[i].Summary = entry
	}
	return nil
}

// environmentHealth is the worst service health of one environment; unknown
// when there is nothing to evaluate or the status read fails.
func (h *projectsHandlers) environmentHealth(ctx context.Context, environmentID uuid.UUID) module.Health {
	status, err := h.reconcile.Status(ctx, environmentID)
	if err != nil || len(status.Services) == 0 {
		return module.HealthUnknown
	}
	worst := module.HealthHealthy
	for _, service := range status.Services {
		if healthRank[service.Health] > healthRank[worst] {
			worst = service.Health
		}
	}
	return worst
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
	}{newProjectPayload(proj, grantFrom(r.Context()))})
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
	}{newProjectPayload(proj, grantFrom(r.Context()))})
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

// POST /v1/projects/{id}/definitions: submit a candidate definition
// version WITHOUT moving the draft. Deployment preparation consumes the
// returned version id; only promotion advances the draft.
func (h *projectsHandlers) submitDefinition(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Source string `json:"source"`
		Format string `json:"format"`
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
	versionID, hash, err := h.projects.SubmitCandidate(r.Context(), id, []byte(req.Source), req.Format)
	if err != nil {
		writeProjectError(r.Context(), w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		DefinitionVersionID string `json:"definition_version_id"`
		DefinitionHash      string `json:"definition_hash"`
	}{versionID.String(), hash})
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
