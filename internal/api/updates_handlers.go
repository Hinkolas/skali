package api

import (
	"errors"
	"net/http"

	"github.com/Hinkolas/skali/internal/clusterstate"
	"github.com/Hinkolas/skali/internal/updates"
)

// updatesHandlers serves the platform update surface: one status document
// the console renders, a manual scan, the settings, and the two mutations
// that hand work to the cluster coordinator (apply, resume). Reads are
// admin-only; mutations additionally sit behind sudo mode.
type updatesHandlers struct {
	updates *updates.Service
}

func (h *updatesHandlers) get(w http.ResponseWriter, r *http.Request) {
	status, err := h.updates.Status(r.Context())
	if err != nil {
		writeInternalError(r.Context(), w, "read update status", err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (h *updatesHandlers) scan(w http.ResponseWriter, r *http.Request) {
	status, err := h.updates.Scan(r.Context())
	if err != nil {
		if errors.Is(err, updates.ErrScanDisabled) {
			writeError(w, http.StatusUnprocessableEntity, codeUpdateBlocked, err.Error())
			return
		}
		writeInternalError(r.Context(), w, "scan for updates", err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (h *updatesHandlers) apply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Version string `json:"version"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	if req.Version == "" {
		writeError(w, http.StatusBadRequest, codeBadRequest, "version is required")
		return
	}
	status, err := h.updates.Apply(r.Context(), req.Version)
	if err != nil {
		h.writeApplyError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, status)
}

func (h *updatesHandlers) resume(w http.ResponseWriter, r *http.Request) {
	status, err := h.updates.Resume(r.Context())
	if err != nil {
		h.writeApplyError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, status)
}

func (h *updatesHandlers) writeApplyError(w http.ResponseWriter, r *http.Request, err error) {
	var blocked *updates.BlockedError
	switch {
	case errors.Is(err, clusterstate.ErrOperationActive):
		writeError(w, http.StatusConflict, codeUpdateInProgress, err.Error())
	case errors.Is(err, updates.ErrBusy), errors.Is(err, updates.ErrNotNewer), errors.As(err, &blocked):
		writeError(w, http.StatusUnprocessableEntity, codeUpdateBlocked, err.Error())
	default:
		writeInternalError(r.Context(), w, "apply update", err)
	}
}

func (h *updatesHandlers) putSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Channel    string `json:"channel"`
		AutoUpdate bool   `json:"auto_update"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	channel, err := updates.ParseChannel(req.Channel)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	status, err := h.updates.UpdateSettings(r.Context(), channel, req.AutoUpdate)
	if err != nil {
		writeInternalError(r.Context(), w, "save update settings", err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}
