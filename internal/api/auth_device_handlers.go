package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Hinkolas/skali/internal/auth"
)

// Browser device authorization (docs/permissions.md, "sudo mode"): the CLI
// creates a request and polls it; the signed-in console looks it up by user
// code and approves or denies. The request and token routes are CLI-only
// (the poll answer carries a CLI bearer token); the code routes are the console's.

// --- payloads (shapes mirror api/openapi.yaml exactly) ---

type deviceRequestPayload struct {
	DeviceCode string    `json:"device_code"`
	UserCode   string    `json:"user_code"`
	ExpiresAt  time.Time `json:"expires_at"`
	Interval   int       `json:"interval"`
}

type devicePollPayload struct {
	Status  string                 `json:"status"`
	Session *sessionCreatedPayload `json:"session,omitempty"`
}

type deviceInfoPayload struct {
	Intent      string    `json:"intent"`
	ClientLabel string    `json:"client_label"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	Mine        bool      `json:"mine"`
}

// --- handlers ---

type deviceStartRequest struct {
	ClientLabel string `json:"client_label"`
}

func (h *authHandlers) writeDeviceRequest(w http.ResponseWriter, req *auth.DeviceRequest) {
	writeJSON(w, http.StatusCreated, deviceRequestPayload{
		DeviceCode: req.DeviceCode,
		UserCode:   req.UserCode,
		ExpiresAt:  req.ExpiresAt,
		Interval:   int(req.Interval / time.Second),
	})
}

// POST /v1/auth/device/requests
func (h *authHandlers) startDeviceLogin(w http.ResponseWriter, r *http.Request) {
	var req deviceStartRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	res, err := h.auth.StartDeviceLogin(r.Context(), req.ClientLabel, h.sessionMeta(r))
	if err != nil {
		writeAuthError(r.Context(), w, err)
		return
	}
	h.writeDeviceRequest(w, res)
}

// POST /v1/auth/device/requests/reauth
func (h *authHandlers) startDeviceReauth(w http.ResponseWriter, r *http.Request) {
	var req deviceStartRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	res, err := h.auth.StartDeviceReauth(r.Context(), UserFrom(r.Context()), SessionFrom(r.Context()).ID, req.ClientLabel, h.sessionMeta(r))
	if err != nil {
		writeAuthError(r.Context(), w, err)
		return
	}
	h.writeDeviceRequest(w, res)
}

// POST /v1/auth/device/token
func (h *authHandlers) pollDevice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceCode string `json:"device_code"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	if req.DeviceCode == "" {
		writeError(w, http.StatusBadRequest, codeBadRequest, "device_code is required")
		return
	}
	res, err := h.auth.PollDevice(r.Context(), req.DeviceCode, clientIP(r))
	if err != nil {
		writeAuthError(r.Context(), w, err)
		return
	}
	payload := devicePollPayload{Status: res.Status}
	if res.Session != nil {
		payload.Session = &sessionCreatedPayload{
			Token:     res.Session.Token,
			ExpiresAt: res.Session.ExpiresAt,
			User:      newUserPayload(&res.Session.User),
		}
	}
	writeJSON(w, http.StatusOK, payload)
}

// GET /v1/auth/device/codes/{user_code}
func (h *authHandlers) lookupDevice(w http.ResponseWriter, r *http.Request) {
	info, err := h.auth.LookupDevice(r.Context(), UserFrom(r.Context()), chi.URLParam(r, "user_code"))
	if err != nil {
		writeAuthError(r.Context(), w, err)
		return
	}
	writeJSON(w, http.StatusOK, deviceInfoPayload{
		Intent:      string(info.Intent),
		ClientLabel: info.ClientLabel,
		CreatedAt:   info.CreatedAt,
		ExpiresAt:   info.ExpiresAt,
		Mine:        info.Mine,
	})
}

// POST /v1/auth/device/codes/{user_code}/approve (sudo mode)
func (h *authHandlers) approveDevice(w http.ResponseWriter, r *http.Request) {
	if err := h.auth.ApproveDevice(r.Context(), UserFrom(r.Context()), chi.URLParam(r, "user_code")); err != nil {
		writeAuthError(r.Context(), w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /v1/auth/device/codes/{user_code}/deny
func (h *authHandlers) denyDevice(w http.ResponseWriter, r *http.Request) {
	if err := h.auth.DenyDevice(r.Context(), UserFrom(r.Context()), chi.URLParam(r, "user_code")); err != nil {
		writeAuthError(r.Context(), w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
