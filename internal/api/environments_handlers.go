package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/Hinkolas/skali/internal/authz"
	"github.com/Hinkolas/skali/internal/deploy"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/store"
)

// environmentsHandlers is the environment surface. Environments hang off a
// project for creation and listing, and are addressed directly otherwise.
// The scope middleware has resolved the caller's grant; a locked
// environment (effective role none) is listed by id and name only.
type environmentsHandlers struct {
	projects *project.Service
	deploy   *deploy.Service
	journal  *journal.Service
}

type environmentPayload struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	// Access is the caller's effective role; none marks a locked
	// environment, which carries nothing below this line.
	Access    string                      `json:"access"`
	CreatedAt *time.Time                  `json:"created_at,omitempty"`
	Settings  *environmentSettingsPayload `json:"settings,omitempty"`
}

type environmentSettingsPayload struct {
	MaxRole      string   `json:"max_role"`
	DeployPolicy string   `json:"deploy_policy"`
	PromoteFrom  []string `json:"promote_from"`
	Priority     string   `json:"priority"`
}

func newEnvironmentSettingsPayload(settings authz.Settings) *environmentSettingsPayload {
	promoteFrom := settings.PromoteFrom
	if promoteFrom == nil {
		promoteFrom = []string{}
	}
	return &environmentSettingsPayload{
		MaxRole:      settings.MaxRole.String(),
		DeployPolicy: settings.DeployPolicy,
		PromoteFrom:  promoteFrom,
		Priority:     settings.Priority,
	}
}

// newEnvironmentPayload renders one environment for a caller with the given
// effective role; a locked one keeps only id, project, name, and access.
func newEnvironmentPayload(e *store.Environment, role authz.Role) environmentPayload {
	payload := environmentPayload{
		ID:        e.ID.String(),
		ProjectID: e.ProjectID.String(),
		Name:      e.Name,
		Access:    role.String(),
	}
	if role == authz.None {
		return payload
	}
	createdAt := e.CreatedAt
	payload.CreatedAt = &createdAt
	if settings, err := authz.SettingsOf(e); err == nil {
		payload.Settings = newEnvironmentSettingsPayload(settings)
	}
	return payload
}

// POST /v1/projects/{id}/environments: project maintain and up; high
// priority is an instance-admin decision. The creator gets an admin cell
// when they are below project admin.
func (h *environmentsHandlers) create(w http.ResponseWriter, r *http.Request) {
	projectID, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Name     string `json:"name"`
		Priority string `json:"priority"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, codeBadRequest, "name is required")
		return
	}
	if req.Priority != "" && !authz.ValidPriority(req.Priority) {
		writeError(w, http.StatusBadRequest, codeBadRequest, "priority must be normal or high")
		return
	}
	if req.Priority == authz.PriorityHigh && !isInstanceAdmin(r) {
		writeError(w, http.StatusForbidden, codeForbidden, "instance admin required to create a high priority environment")
		return
	}
	user := UserFrom(r.Context())
	env, err := h.projects.CreateEnvironment(r.Context(), projectID, req.Name, project.EnvironmentOptions{
		Creator:  user.ID,
		Priority: req.Priority,
	})
	if err != nil {
		writeProjectError(r.Context(), w, err)
		return
	}
	// The creator's role on the new environment: admin by project role or
	// by the cell just granted, which is exactly what the grant resolves.
	role := authz.Admin
	writeJSON(w, http.StatusCreated, struct {
		Environment environmentPayload `json:"environment"`
	}{newEnvironmentPayload(env, role)})
}

// GET /v1/projects/{id}/environments: every environment of the project,
// locked ones by id and name only.
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
	grant := grantFrom(r.Context())
	payload := make([]environmentPayload, len(environments))
	for i := range environments {
		role := authz.None
		if envGrant, ok := grant.Environment(environments[i].ID); ok {
			role = envGrant.Role
		}
		payload[i] = newEnvironmentPayload(&environments[i], role)
	}
	writeJSON(w, http.StatusOK, struct {
		Environments []environmentPayload `json:"environments"`
	}{payload})
}

// GET /v1/environments/{id}: the minimal shape for a locked environment.
func (h *environmentsHandlers) get(w http.ResponseWriter, r *http.Request) {
	env := environmentFrom(r.Context())
	envGrant := environmentGrantFrom(r.Context())
	writeJSON(w, http.StatusOK, struct {
		Environment environmentPayload `json:"environment"`
	}{newEnvironmentPayload(env, envGrant.Role)})
}

// PATCH /v1/environments/{id}: settings, partial. Environment admin;
// raising priority to high needs an instance admin.
func (h *environmentsHandlers) update(w http.ResponseWriter, r *http.Request) {
	env := environmentFrom(r.Context())
	var req struct {
		MaxRole      *string   `json:"max_role"`
		DeployPolicy *string   `json:"deploy_policy"`
		PromoteFrom  *[]string `json:"promote_from"`
		Priority     *string   `json:"priority"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	if req.MaxRole == nil && req.DeployPolicy == nil && req.PromoteFrom == nil && req.Priority == nil {
		writeError(w, http.StatusBadRequest, codeBadRequest,
			"nothing to update: provide max_role, deploy_policy, promote_from, and/or priority")
		return
	}
	settings, err := authz.SettingsOf(env)
	if err != nil {
		writeInternalError(r.Context(), w, "decode environment settings", err)
		return
	}
	if req.MaxRole != nil {
		role, ok := authz.ParseRole(*req.MaxRole)
		if !ok {
			writeError(w, http.StatusBadRequest, codeBadRequest, "max_role must be one of none, read, deploy, maintain, admin")
			return
		}
		settings.MaxRole = role
	}
	if req.DeployPolicy != nil {
		settings.DeployPolicy = *req.DeployPolicy
	}
	if req.PromoteFrom != nil {
		settings.PromoteFrom = *req.PromoteFrom
	}
	if req.Priority != nil {
		if *req.Priority == authz.PriorityHigh && settings.Priority != authz.PriorityHigh && !isInstanceAdmin(r) {
			writeError(w, http.StatusForbidden, codeForbidden, "instance admin required to raise an environment to high priority")
			return
		}
		settings.Priority = *req.Priority
	}
	updated, err := h.projects.UpdateEnvironmentSettings(r.Context(), env.ID, settings)
	if err != nil {
		writeProjectError(r.Context(), w, err)
		return
	}
	logAccessChange(r, "environment settings changed", "environment", env.ID.String(),
		map[string]any{"max_role": env.MaxRole, "deploy_policy": env.DeployPolicy, "promote_from": env.PromoteFrom, "priority": env.Priority},
		map[string]any{"max_role": updated.MaxRole, "deploy_policy": updated.DeployPolicy, "promote_from": updated.PromoteFrom, "priority": updated.Priority})
	writeJSON(w, http.StatusOK, struct {
		Environment environmentPayload `json:"environment"`
	}{newEnvironmentPayload(updated, environmentGrantFrom(r.Context()).Role)})
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
