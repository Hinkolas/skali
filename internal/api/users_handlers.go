package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/auth"
	"github.com/Hinkolas/skali/internal/store"
)

// usersHandlers is the admin-only user-management surface (the router wraps
// every route in RequireAdmin). The last-admin invariant lives in
// internal/auth; the self-targeting guards live here because they need the
// caller's identity.
type usersHandlers struct {
	st *store.Store
}

// pathUserID parses the {id} route param, writing a 404 on malformed ids so
// they are indistinguishable from missing users.
func pathUserID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return uuid.UUID{}, false
	}
	return id, true
}

// GET /v1/users
func (h *usersHandlers) list(w http.ResponseWriter, r *http.Request) {
	users, err := h.st.ListUsers(r.Context())
	if err != nil {
		writeAuthError(r.Context(), w, err)
		return
	}
	payload := make([]userPayload, len(users))
	for i := range users {
		payload[i] = newUserPayload(&users[i])
	}
	writeJSON(w, http.StatusOK, struct {
		Users []userPayload `json:"users"`
	}{payload})
}

// POST /v1/users
func (h *usersHandlers) create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email          string `json:"email"`
		Name           string `json:"name"`
		Password       string `json:"password"`
		Role           string `json:"role"`
		CreateProjects bool   `json:"create_projects"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	if req.Email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, codeBadRequest, "email and password are required")
		return
	}
	if req.Role == "" {
		req.Role = auth.RoleMember
	}

	user, err := auth.CreateUser(r.Context(), h.st, req.Email, req.Name, req.Password, req.Role)
	if err != nil {
		writeAuthError(r.Context(), w, err)
		return
	}
	if req.CreateProjects {
		updated, err := auth.SetUserCreateProjects(r.Context(), h.st, user.ID, true)
		if err != nil {
			writeAuthError(r.Context(), w, err)
			return
		}
		user = &updated
	}
	writeJSON(w, http.StatusCreated, struct {
		User userPayload `json:"user"`
	}{newUserPayload(user)})
}

// PATCH /v1/users/{id}
func (h *usersHandlers) update(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUserID(w, r)
	if !ok {
		return
	}
	var req struct {
		Name           *string `json:"name"`
		Role           *string `json:"role"`
		CreateProjects *bool   `json:"create_projects"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	if req.Name == nil && req.Role == nil && req.CreateProjects == nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, "nothing to update: provide name, role, and/or create_projects")
		return
	}
	// Changing your own role is refused outright: it either locks you out of
	// this very UI or trips the last-admin guard — another admin must do it.
	if req.Role != nil && id == UserFrom(r.Context()).ID {
		writeError(w, http.StatusConflict, codeConflict, "you cannot change your own role")
		return
	}

	var user store.User
	var err error
	if req.Name != nil {
		if user, err = auth.SetUserName(r.Context(), h.st, id, *req.Name); err != nil {
			writeAuthError(r.Context(), w, err)
			return
		}
	}
	if req.Role != nil {
		if user, err = auth.SetUserRole(r.Context(), h.st, id, *req.Role); err != nil {
			writeAuthError(r.Context(), w, err)
			return
		}
	}
	if req.CreateProjects != nil {
		if user, err = auth.SetUserCreateProjects(r.Context(), h.st, id, *req.CreateProjects); err != nil {
			writeAuthError(r.Context(), w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, struct {
		User userPayload `json:"user"`
	}{newUserPayload(&user)})
}

// DELETE /v1/users/{id}
func (h *usersHandlers) delete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUserID(w, r)
	if !ok {
		return
	}
	if id == UserFrom(r.Context()).ID {
		writeError(w, http.StatusConflict, codeConflict, "you cannot delete your own account")
		return
	}
	if err := auth.DeleteUser(r.Context(), h.st, id); err != nil {
		writeAuthError(r.Context(), w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /v1/users/{id}/password
func (h *usersHandlers) resetPassword(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUserID(w, r)
	if !ok {
		return
	}
	var req struct {
		NewPassword string `json:"new_password"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	if req.NewPassword == "" {
		writeError(w, http.StatusBadRequest, codeBadRequest, "new_password is required")
		return
	}
	if err := auth.ResetUserPassword(r.Context(), h.st, id, req.NewPassword); err != nil {
		writeAuthError(r.Context(), w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
