package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/store"
)

// inventoryHandlers is the admin-only read surface over per-node image and
// volume inventory. Pure observed state from the heartbeat recorder — no ops
// service, no live node calls; mutations (pull, remove, GC) arrive in a
// later milestone.
type inventoryHandlers struct {
	st *store.Store
}

type imagePayload struct {
	ID          string     `json:"id"`
	RepoTags    []string   `json:"repo_tags"`
	RepoDigests []string   `json:"repo_digests"`
	SizeBytes   int64      `json:"size_bytes"`
	Dangling    bool       `json:"dangling"`
	Containers  int32      `json:"containers"`
	CreatedAt   *time.Time `json:"created_at"`
	FirstSeen   time.Time  `json:"first_seen"`
	LastSeen    time.Time  `json:"last_seen"`
}

type volumePayload struct {
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	Scope      string            `json:"scope"`
	Mountpoint string            `json:"mountpoint"`
	Labels     map[string]string `json:"labels"`
	Containers int32             `json:"containers"`
	CreatedAt  *time.Time        `json:"created_at"`
	FirstSeen  time.Time         `json:"first_seen"`
	LastSeen   time.Time         `json:"last_seen"`
}

func newImagePayload(img *store.NodeImage) imagePayload {
	return imagePayload{
		ID:          img.ImageID,
		RepoTags:    img.RepoTags,
		RepoDigests: img.RepoDigests,
		SizeBytes:   img.SizeBytes,
		Dangling:    img.Dangling,
		Containers:  img.Containers,
		CreatedAt:   img.ImageCreated,
		FirstSeen:   img.FirstSeen,
		LastSeen:    img.LastSeen,
	}
}

func newVolumePayload(v *store.NodeVolume) volumePayload {
	p := volumePayload{
		Name:       v.Name,
		Driver:     v.Driver,
		Scope:      v.Scope,
		Mountpoint: v.Mountpoint,
		Containers: v.Containers,
		CreatedAt:  v.VolumeCreated,
		FirstSeen:  v.FirstSeen,
		LastSeen:   v.LastSeen,
	}
	if err := json.Unmarshal(v.Labels, &p.Labels); err != nil || p.Labels == nil {
		p.Labels = map[string]string{}
	}
	return p
}

// requireNode 404s for unknown nodes so an empty inventory is
// distinguishable from a typoed id.
func (h *inventoryHandlers) requireNode(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	nodeID, ok := pathNodeID(w, r)
	if !ok {
		return nodeID, false
	}
	if _, err := h.st.GetNodeByID(r.Context(), nodeID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, codeNotFound, "not found")
			return nodeID, false
		}
		writeClusterError(r.Context(), w, err)
		return nodeID, false
	}
	return nodeID, true
}

// GET /v1/nodes/{id}/images
func (h *inventoryHandlers) images(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireNode(w, r)
	if !ok {
		return
	}
	rows, err := h.st.ListNodeImages(r.Context(), id)
	if err != nil {
		writeClusterError(r.Context(), w, err)
		return
	}
	payload := make([]imagePayload, len(rows))
	for i := range rows {
		payload[i] = newImagePayload(&rows[i])
	}
	writeJSON(w, http.StatusOK, struct {
		Images []imagePayload `json:"images"`
	}{payload})
}

// GET /v1/nodes/{id}/volumes
func (h *inventoryHandlers) volumes(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireNode(w, r)
	if !ok {
		return
	}
	rows, err := h.st.ListNodeVolumes(r.Context(), id)
	if err != nil {
		writeClusterError(r.Context(), w, err)
		return
	}
	payload := make([]volumePayload, len(rows))
	for i := range rows {
		payload[i] = newVolumePayload(&rows[i])
	}
	writeJSON(w, http.StatusOK, struct {
		Volumes []volumePayload `json:"volumes"`
	}{payload})
}
