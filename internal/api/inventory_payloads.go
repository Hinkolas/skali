package api

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/store"
)

// Payload shapes for the inventory half of the containers surface (images
// and volumes) — pure observed state from the heartbeat recorder; mutations
// (pull, remove, GC) arrive in a later milestone.

type imagePayload struct {
	ID          string     `json:"id"`
	NodeID      uuid.UUID  `json:"node_id"`
	NodeName    string     `json:"node_name"`
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
	NodeID     uuid.UUID         `json:"node_id"`
	NodeName   string            `json:"node_name"`
	Driver     string            `json:"driver"`
	Scope      string            `json:"scope"`
	Mountpoint string            `json:"mountpoint"`
	Labels     map[string]string `json:"labels"`
	Containers int32             `json:"containers"`
	CreatedAt  *time.Time        `json:"created_at"`
	FirstSeen  time.Time         `json:"first_seen"`
	LastSeen   time.Time         `json:"last_seen"`
}

func newImagePayload(img *store.NodeImage, nodeName string) imagePayload {
	return imagePayload{
		ID:          img.ImageID,
		NodeID:      img.NodeID,
		NodeName:    nodeName,
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

func newVolumePayload(v *store.NodeVolume, nodeName string) volumePayload {
	p := volumePayload{
		Name:       v.Name,
		NodeID:     v.NodeID,
		NodeName:   nodeName,
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
