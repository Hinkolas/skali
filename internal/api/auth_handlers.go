package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/auth"
	"github.com/Hinkolas/skali/internal/store"
)

type authHandlers struct {
	auth *auth.Service
}

// --- payloads (shapes mirror api/openapi.yaml exactly) ---

type userPayload struct {
	ID               string    `json:"id"`
	Email            string    `json:"email"`
	Name             string    `json:"name"`
	Role             string    `json:"role"`
	TwoFactorEnabled bool      `json:"two_factor_enabled"`
	CreatedAt        time.Time `json:"created_at"`
}

func newUserPayload(u *store.User) userPayload {
	return userPayload{
		ID:               u.ID.String(),
		Email:            u.Email,
		Name:             u.Name,
		Role:             u.Role,
		TwoFactorEnabled: u.TwoFactorEnabled,
		CreatedAt:        u.CreatedAt,
	}
}

// sessionCreatedPayload is the one and only place a bearer token leaves the
// server.
type sessionCreatedPayload struct {
	Token     string      `json:"token"`
	ExpiresAt time.Time   `json:"expires_at"`
	User      userPayload `json:"user"`
}

type sessionPayload struct {
	ID        string    `json:"id"`
	ExpiresAt time.Time `json:"expires_at"`
	IPAddress string    `json:"ip_address"`
	UserAgent string    `json:"user_agent"`
	CreatedAt time.Time `json:"created_at"`
	Current   bool      `json:"current"`
}

func newSessionPayload(s *store.Session, currentID uuid.UUID) sessionPayload {
	return sessionPayload{
		ID:        s.ID.String(),
		ExpiresAt: s.ExpiresAt,
		IPAddress: s.IpAddress,
		UserAgent: s.UserAgent,
		CreatedAt: s.CreatedAt,
		Current:   s.ID == currentID,
	}
}

type challengePayload struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// --- handlers ---

func (h *authHandlers) sessionMeta(r *http.Request) auth.SessionMeta {
	ua := r.UserAgent()
	if len(ua) > 512 {
		ua = ua[:512]
	}
	return auth.SessionMeta{IPAddress: clientIP(r), UserAgent: ua}
}

// POST /v1/auth/login
func (h *authHandlers) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	if req.Email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, codeBadRequest, "email and password are required")
		return
	}

	res, err := h.auth.Login(r.Context(), req.Email, req.Password, h.sessionMeta(r))
	if err != nil {
		writeAuthError(r.Context(), w, err)
		return
	}
	if res.Challenge != nil {
		writeJSON(w, http.StatusOK, struct {
			Challenge challengePayload `json:"challenge"`
		}{challengePayload{Token: res.Challenge.Token, ExpiresAt: res.Challenge.ExpiresAt}})
		return
	}
	h.writeSessionCreated(w, res.Session)
}

// POST /v1/auth/2fa/verify
func (h *authHandlers) verifyTwoFactor(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ChallengeToken string `json:"challenge_token"`
		Code           string `json:"code"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	if req.ChallengeToken == "" || req.Code == "" {
		writeError(w, http.StatusBadRequest, codeBadRequest, "challenge_token and code are required")
		return
	}

	sess, err := h.auth.VerifyTwoFactor(r.Context(), req.ChallengeToken, req.Code, h.sessionMeta(r))
	if err != nil {
		writeAuthError(r.Context(), w, err)
		return
	}
	h.writeSessionCreated(w, sess)
}

func (h *authHandlers) writeSessionCreated(w http.ResponseWriter, sess *auth.Session) {
	writeJSON(w, http.StatusOK, struct {
		Session sessionCreatedPayload `json:"session"`
	}{sessionCreatedPayload{
		Token:     sess.Token,
		ExpiresAt: sess.ExpiresAt,
		User:      newUserPayload(&sess.User),
	}})
}

// POST /v1/auth/logout
func (h *authHandlers) logout(w http.ResponseWriter, r *http.Request) {
	if err := h.auth.Logout(r.Context(), tokenFrom(r.Context())); err != nil {
		writeAuthError(r.Context(), w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /v1/auth/session
func (h *authHandlers) currentSession(w http.ResponseWriter, r *http.Request) {
	user := UserFrom(r.Context())
	sess := SessionFrom(r.Context())
	writeJSON(w, http.StatusOK, struct {
		User    userPayload    `json:"user"`
		Session sessionPayload `json:"session"`
	}{newUserPayload(user), newSessionPayload(sess, sess.ID)})
}

// GET /v1/auth/sessions
func (h *authHandlers) listSessions(w http.ResponseWriter, r *http.Request) {
	user := UserFrom(r.Context())
	current := SessionFrom(r.Context())

	sessions, err := h.auth.ListSessions(r.Context(), user.ID)
	if err != nil {
		writeAuthError(r.Context(), w, err)
		return
	}
	payload := make([]sessionPayload, len(sessions))
	for i := range sessions {
		payload[i] = newSessionPayload(&sessions[i], current.ID)
	}
	writeJSON(w, http.StatusOK, struct {
		Sessions []sessionPayload `json:"sessions"`
	}{payload})
}

// DELETE /v1/auth/sessions/{id}
func (h *authHandlers) revokeSession(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return
	}
	if err := h.auth.RevokeSession(r.Context(), UserFrom(r.Context()).ID, id); err != nil {
		writeAuthError(r.Context(), w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /v1/auth/password
func (h *authHandlers) changePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	if req.CurrentPassword == "" || req.NewPassword == "" {
		writeError(w, http.StatusBadRequest, codeBadRequest, "current_password and new_password are required")
		return
	}

	err := h.auth.ChangePassword(r.Context(), UserFrom(r.Context()).ID, req.CurrentPassword, req.NewPassword, SessionFrom(r.Context()).ID)
	if err != nil {
		writeAuthError(r.Context(), w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /v1/auth/2fa/enable
func (h *authHandlers) enableTwoFactor(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	enr, err := h.auth.EnableTwoFactor(r.Context(), UserFrom(r.Context()).ID, req.Password)
	if err != nil {
		writeAuthError(r.Context(), w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Secret      string   `json:"secret"`
		OTPAuthURI  string   `json:"otpauth_uri"`
		BackupCodes []string `json:"backup_codes"`
	}{enr.Secret, enr.OTPAuthURI, enr.BackupCodes})
}

// POST /v1/auth/2fa/confirm
func (h *authHandlers) confirmTwoFactor(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	if err := h.auth.ConfirmTwoFactor(r.Context(), UserFrom(r.Context()).ID, req.Code); err != nil {
		writeAuthError(r.Context(), w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /v1/auth/2fa/disable
func (h *authHandlers) disableTwoFactor(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	if err := h.auth.DisableTwoFactor(r.Context(), UserFrom(r.Context()).ID, req.Password, req.Code); err != nil {
		writeAuthError(r.Context(), w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /v1/auth/2fa/backup-codes
func (h *authHandlers) regenerateBackupCodes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	codes, err := h.auth.RegenerateBackupCodes(r.Context(), UserFrom(r.Context()).ID, req.Password)
	if err != nil {
		writeAuthError(r.Context(), w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		BackupCodes []string `json:"backup_codes"`
	}{codes})
}
