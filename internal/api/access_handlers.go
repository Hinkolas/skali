package api

import (
	"log/slog"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/authz"
	"github.com/Hinkolas/skali/internal/project"
)

// accessHandlers manages who holds which role: project members and
// per-environment cells. Reads need project read (members) or environment
// read (cells); writes need the respective admin and sudo mode. The {user}
// segment is a user id or an email, so project admins can grant by the
// address they know without listing users (an instance-admin right).
type accessHandlers struct {
	projects *project.Service
}

type memberPayload struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Name   string `json:"name"`
	Role   string `json:"role"`
}

func newMemberPayload(m *project.Member) memberPayload {
	return memberPayload{UserID: m.UserID.String(), Email: m.Email, Name: m.Name, Role: m.Role.String()}
}

func newCellPayload(c *project.Cell) memberPayload {
	return memberPayload{UserID: c.UserID.String(), Email: c.Email, Name: c.Name, Role: c.Role.String()}
}

// pathUser resolves the {user} segment to a user id; 404 when unknown.
func (h *accessHandlers) pathUser(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	raw, err := url.PathUnescape(chi.URLParam(r, "user"))
	if err != nil || raw == "" {
		writeError(w, http.StatusNotFound, codeNotFound, "user not found")
		return uuid.Nil, false
	}
	user, err := h.projects.ResolveUser(r.Context(), raw)
	if err != nil {
		writeProjectError(r.Context(), w, err)
		return uuid.Nil, false
	}
	return user.ID, true
}

func decodeRole(w http.ResponseWriter, r *http.Request) (authz.Role, bool) {
	var req struct {
		Role string `json:"role"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return 0, false
	}
	role, ok := authz.ParseRole(req.Role)
	if !ok {
		writeError(w, http.StatusBadRequest, codeBadRequest, "role must be one of none, read, deploy, maintain, admin")
		return 0, false
	}
	return role, true
}

// GET /v1/projects/{id}/members
func (h *accessHandlers) listMembers(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	members, err := h.projects.ListMembers(r.Context(), id)
	if err != nil {
		writeProjectError(r.Context(), w, err)
		return
	}
	payload := make([]memberPayload, len(members))
	for i := range members {
		payload[i] = newMemberPayload(&members[i])
	}
	writeJSON(w, http.StatusOK, struct {
		Members []memberPayload `json:"members"`
	}{payload})
}

// PUT /v1/projects/{id}/members/{user}: add or change a membership.
func (h *accessHandlers) putMember(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	userID, ok := h.pathUser(w, r)
	if !ok {
		return
	}
	role, ok := decodeRole(w, r)
	if !ok {
		return
	}
	if !authz.ValidProjectRole(role.String()) {
		writeError(w, http.StatusBadRequest, codeBadRequest, "project roles are read, deploy, maintain, admin; remove the membership instead of none")
		return
	}
	uid := userID
	member, err := h.projects.SetMember(r.Context(), id, uid, role)
	if err != nil {
		writeProjectError(r.Context(), w, err)
		return
	}
	logAccessChange(r, "project member set", "project", id.String(), map[string]any{"user": member.Email}, map[string]any{"user": member.Email, "role": role.String()})
	writeJSON(w, http.StatusOK, struct {
		Member memberPayload `json:"member"`
	}{newMemberPayload(member)})
}

// DELETE /v1/projects/{id}/members/{user}: the user's cells go with it.
func (h *accessHandlers) deleteMember(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	userID, ok := h.pathUser(w, r)
	if !ok {
		return
	}
	if err := h.projects.RemoveMember(r.Context(), id, userID); err != nil {
		if err == project.ErrNotMember {
			writeError(w, http.StatusNotFound, codeNotFound, "not a member")
			return
		}
		writeProjectError(r.Context(), w, err)
		return
	}
	logAccessChange(r, "project member removed", "project", id.String(), map[string]any{"user": userID.String()}, nil)
	w.WriteHeader(http.StatusNoContent)
}

// GET /v1/environments/{id}/access
func (h *accessHandlers) listEnvironmentAccess(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	cells, err := h.projects.ListEnvironmentAccess(r.Context(), id)
	if err != nil {
		writeProjectError(r.Context(), w, err)
		return
	}
	payload := make([]memberPayload, len(cells))
	for i := range cells {
		payload[i] = newCellPayload(&cells[i])
	}
	writeJSON(w, http.StatusOK, struct {
		Access []memberPayload `json:"access"`
	}{payload})
}

// PUT /v1/environments/{id}/access/{user}: set a cell; the user must be a
// member of the project.
func (h *accessHandlers) putEnvironmentAccess(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	userID, ok := h.pathUser(w, r)
	if !ok {
		return
	}
	role, ok := decodeRole(w, r)
	if !ok {
		return
	}
	cell, err := h.projects.SetEnvironmentAccess(r.Context(), id, userID, role)
	if err != nil {
		writeProjectError(r.Context(), w, err)
		return
	}
	logAccessChange(r, "environment access set", "environment", id.String(), map[string]any{"user": cell.Email}, map[string]any{"user": cell.Email, "role": role.String()})
	writeJSON(w, http.StatusOK, struct {
		Access memberPayload `json:"access"`
	}{newCellPayload(cell)})
}

// DELETE /v1/environments/{id}/access/{user}: the user falls back to the
// project role.
func (h *accessHandlers) deleteEnvironmentAccess(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	userID, ok := h.pathUser(w, r)
	if !ok {
		return
	}
	if err := h.projects.RemoveEnvironmentAccess(r.Context(), id, userID); err != nil {
		if err == project.ErrEnvironmentNotFound {
			writeError(w, http.StatusNotFound, codeNotFound, "no access entry for this user")
			return
		}
		writeProjectError(r.Context(), w, err)
		return
	}
	logAccessChange(r, "environment access removed", "environment", id.String(), map[string]any{"user": userID.String()}, nil)
	w.WriteHeader(http.StatusNoContent)
}

// logAccessChange records who changed which access or setting, with the
// before and after values. A durable audit table is deferred until
// something needs to read it.
func logAccessChange(r *http.Request, event, targetKind, targetID string, before, after map[string]any) {
	actor := ""
	if user := UserFrom(r.Context()); user != nil {
		actor = user.Email
	}
	slog.InfoContext(r.Context(), "access: "+event,
		"actor", actor, targetKind, targetID, "before", before, "after", after)
}
