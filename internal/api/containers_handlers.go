package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/store"
)

// containersHandlers is the admin-only read-only engine surface: what the
// node engines report, cluster-wide, verbatim — the drift-debugging window.
// All writes go through workloads (and the registry); the engine has no
// user-facing imperative surface anymore.
type containersHandlers struct {
	st *store.Store
}

type containerStatsPayload struct {
	// CPUPct uses docker-stats semantics: 100 = one full core.
	CPUPct    float32 `json:"cpu_pct"`
	MemUsed   int64   `json:"mem_used"`
	MemLimit  int64   `json:"mem_limit"`
	NetRxRate int64   `json:"net_rx_rate"`
	NetTxRate int64   `json:"net_tx_rate"`
}

type containerPayload struct {
	ID           string                 `json:"id"`
	NodeID       uuid.UUID              `json:"node_id"`
	NodeName     string                 `json:"node_name"`
	Name         string                 `json:"name"`
	Image        string                 `json:"image"`
	Kind         string                 `json:"kind"`
	State        string                 `json:"state"`
	Health       *string                `json:"health"`
	ExitCode     *int32                 `json:"exit_code"`
	RestartCount int32                  `json:"restart_count"`
	Labels       map[string]string      `json:"labels"`
	Stats        *containerStatsPayload `json:"stats"`
	CreatedAt    *time.Time             `json:"created_at"`
	StartedAt    *time.Time             `json:"started_at"`
	FirstSeen    time.Time              `json:"first_seen"`
	LastSeen     time.Time              `json:"last_seen"`
}

func newContainerPayload(c *store.NodeContainer, nodeName string) containerPayload {
	p := containerPayload{
		ID:           c.ContainerID,
		NodeID:       c.NodeID,
		NodeName:     nodeName,
		Name:         c.Name,
		Image:        c.Image,
		Kind:         c.Kind,
		State:        c.State,
		Health:       c.Health,
		ExitCode:     c.ExitCode,
		RestartCount: c.RestartCount,
		CreatedAt:    c.ContainerCreated,
		StartedAt:    c.ContainerStarted,
		FirstSeen:    c.FirstSeen,
		LastSeen:     c.LastSeen,
	}
	if err := json.Unmarshal(c.Labels, &p.Labels); err != nil {
		p.Labels = map[string]string{}
	}
	// Stats columns are written atomically; one sentinel decides presence.
	if c.CpuPct != nil {
		p.Stats = &containerStatsPayload{
			CPUPct:    *c.CpuPct,
			MemUsed:   deref(c.MemUsed),
			MemLimit:  deref(c.MemLimit),
			NetRxRate: deref(c.NetRxRate),
			NetTxRate: deref(c.NetTxRate),
		}
	}
	return p
}

// GET /v1/containers?node=
func (h *containersHandlers) list(w http.ResponseWriter, r *http.Request) {
	filter, ok := queryNodeFilter(w, r, h.st)
	if !ok {
		return
	}
	rows, err := h.st.ListContainers(r.Context(), filter)
	if err != nil {
		writeClusterError(r.Context(), w, err)
		return
	}
	payload := make([]containerPayload, len(rows))
	for i := range rows {
		payload[i] = newContainerPayload(&rows[i].NodeContainer, rows[i].NodeName)
	}
	writeJSON(w, http.StatusOK, struct {
		Containers []containerPayload `json:"containers"`
	}{payload})
}

// GET /v1/images?node=
func (h *containersHandlers) images(w http.ResponseWriter, r *http.Request) {
	filter, ok := queryNodeFilter(w, r, h.st)
	if !ok {
		return
	}
	rows, err := h.st.ListImages(r.Context(), filter)
	if err != nil {
		writeClusterError(r.Context(), w, err)
		return
	}
	payload := make([]imagePayload, len(rows))
	for i := range rows {
		payload[i] = newImagePayload(&rows[i].NodeImage, rows[i].NodeName)
	}
	writeJSON(w, http.StatusOK, struct {
		Images []imagePayload `json:"images"`
	}{payload})
}

// GET /v1/volumes?node=
func (h *containersHandlers) volumes(w http.ResponseWriter, r *http.Request) {
	filter, ok := queryNodeFilter(w, r, h.st)
	if !ok {
		return
	}
	rows, err := h.st.ListVolumes(r.Context(), filter)
	if err != nil {
		writeClusterError(r.Context(), w, err)
		return
	}
	payload := make([]volumePayload, len(rows))
	for i := range rows {
		payload[i] = newVolumePayload(&rows[i].NodeVolume, rows[i].NodeName)
	}
	writeJSON(w, http.StatusOK, struct {
		Volumes []volumePayload `json:"volumes"`
	}{payload})
}
