package api

import "net/http"

// systemHandlers serves instance-level facts that are configuration, not
// observation: the daemon version, the operator-chosen instance name, and
// the installation identity clients pin to detect a reinstall.
type systemHandlers struct {
	version      string
	instanceName string
	instanceID   string
}

type systemMetaPayload struct {
	Version    string `json:"version"`
	Name       string `json:"name,omitempty"`
	InstanceID string `json:"instance_id,omitempty"`
}

func (h *systemHandlers) meta(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, systemMetaPayload{
		Version:    h.version,
		Name:       h.instanceName,
		InstanceID: h.instanceID,
	})
}
