package api

import "net/http"

// systemHandlers serves instance-level facts that are configuration, not
// observation: the daemon version and the operator-chosen instance name.
type systemHandlers struct {
	version      string
	instanceName string
}

type systemMetaPayload struct {
	Version string `json:"version"`
	Name    string `json:"name,omitempty"`
}

func (h *systemHandlers) meta(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, systemMetaPayload{
		Version: h.version,
		Name:    h.instanceName,
	})
}
