package api

import (
	"net/http"

	"github.com/Hinkolas/skali/internal/updates"
)

// systemHandlers serves instance-level facts that are configuration, not
// observation: the daemon version, the operator-chosen instance name, the
// installation identity clients pin to detect a reinstall, and whether a
// newer release is known, so the shell can show its indicator without a
// second request.
type systemHandlers struct {
	version      string
	instanceName string
	instanceID   string
	updates      *updates.Service
}

type systemMetaPayload struct {
	Version         string           `json:"version"`
	Name            string           `json:"name,omitempty"`
	InstanceID      string           `json:"instance_id,omitempty"`
	UpdateAvailable *updates.Release `json:"update_available,omitempty"`
}

func (h *systemHandlers) meta(w http.ResponseWriter, r *http.Request) {
	payload := systemMetaPayload{
		Version:    h.version,
		Name:       h.instanceName,
		InstanceID: h.instanceID,
	}
	if h.updates != nil {
		// Best effort: meta must answer even when the settings row cannot
		// be read, and the indicator is a hint, not a fact worth failing on.
		// The hint is memoized in the service: meta runs on every shell
		// load, and the full status document is the Updates page's to ask.
		if release, err := h.updates.UpdateHint(r.Context()); err == nil {
			payload.UpdateAvailable = release
		}
	}
	writeJSON(w, http.StatusOK, payload)
}
