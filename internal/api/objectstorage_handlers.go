package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/reconcile"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
)

// ObjectStorage is the substrate's object-store surface behind the object
// storage routes. *substrate.Controller implements it; nil (API-only mode)
// reports the capacity as unknown and leaves a saved change for the
// substrate's next resync.
type ObjectStorage interface {
	// ObjectStoreSlots reads the volume servers' slot accounting from the
	// master.
	ObjectStoreSlots(ctx context.Context) (seaweed.SlotReport, error)
	// EnqueueObjectStoreSettings re-applies the store now instead of at
	// the next resync.
	EnqueueObjectStoreSettings()
}

type objectStorageHandlers struct {
	db      *dbstore.Service
	storage ObjectStorage
	// reconcile serves the provider observation; nil reports none.
	reconcile *reconcile.Kernel
	managed   bool
}

// objectStoreVolumeSizePayload is the volume size limit: what the masters
// run with, whether that is the default, and the bounds a change must
// stay within.
type objectStoreVolumeSizePayload struct {
	LimitMB   int  `json:"limit_mb"`
	Default   bool `json:"default"`
	DefaultMB int  `json:"default_mb"`
	MinMB     int  `json:"min_mb"`
	MaxMB     int  `json:"max_mb"`
}

// objectStoreServerPayload is one volume server's slots.
type objectStoreServerPayload struct {
	URL       string `json:"url"`
	Slots     int    `json:"slots"`
	UsedSlots int    `json:"used_slots"`
	FreeSlots int    `json:"free_slots"`
}

// objectStoreCapacityPayload is the store's live slot accounting. Every
// bucket is its own collection holding at least volumes_per_bucket
// volumes with copies replicas each, so slots, not bytes, decide how many
// buckets fit: bucket_headroom is how many more can be created before a
// new bucket's first write finds no room, an estimate since existing
// buckets take slots as they fill their volumes.
type objectStoreCapacityPayload struct {
	Slots            int                        `json:"slots"`
	UsedSlots        int                        `json:"used_slots"`
	FreeSlots        int                        `json:"free_slots"`
	VolumeHeadroom   int                        `json:"volume_headroom"`
	BucketHeadroom   int                        `json:"bucket_headroom"`
	VolumesPerBucket int                        `json:"volumes_per_bucket"`
	Copies           int                        `json:"copies"`
	Servers          []objectStoreServerPayload `json:"servers"`
}

// objectStoreObservedPayload is the provider observer's newest pass; state
// is fresh, stale or unknown.
type objectStoreObservedPayload struct {
	State                     string     `json:"state"`
	StaleSince                *time.Time `json:"stale_since"`
	MastersReady              int32      `json:"masters_ready"`
	VolumeServersReady        int32      `json:"volume_servers_ready"`
	FilerReady                bool       `json:"filer_ready"`
	S3Ready                   bool       `json:"s3_ready"`
	S3Detail                  string     `json:"s3_detail,omitempty"`
	UnderReplicatedVolumes    int32      `json:"under_replicated_volumes"`
	ReplicationPendingVolumes int32      `json:"replication_pending_volumes"`
	// VolumeSizeLimitMB is the limit the store was observed under; it
	// trails a change until the next pass.
	VolumeSizeLimitMB int32 `json:"volume_size_limit_mb"`
}

type objectStorePayload struct {
	Name          string                       `json:"name"`
	State         string                       `json:"state"`
	Image         string                       `json:"image"`
	Managed       bool                         `json:"managed"`
	Masters       int32                        `json:"masters"`
	VolumeServers int32                        `json:"volume_servers"`
	Replication   string                       `json:"replication"`
	VolumeSize    objectStoreVolumeSizePayload `json:"volume_size"`
	// Buckets counts the live buckets allocated on the store.
	Buckets int64 `json:"buckets"`
	// Capacity is null when the master could not be read (or no
	// substrate runs); the page then shows the setting without numbers.
	Capacity  *objectStoreCapacityPayload `json:"capacity"`
	Observed  *objectStoreObservedPayload `json:"observed"`
	CreatedAt time.Time                   `json:"created_at"`
	UpdatedAt time.Time                   `json:"updated_at"`
}

// liveStore resolves the installation's store or writes the 404: a
// platform without the object-storage capability has none.
func (h *objectStorageHandlers) liveStore(w http.ResponseWriter, r *http.Request) (*store.ObjectStore, bool) {
	row, err := h.db.LiveObjectStore(r.Context())
	if errors.Is(err, dbstore.ErrNotFound) {
		writeError(w, http.StatusNotFound, codeNotFound, "no object store on this installation")
		return nil, false
	}
	if err != nil {
		writeInternalError(r.Context(), w, "get object store", err)
		return nil, false
	}
	return row, true
}

// payload projects the store with its live capacity. A failed master
// read degrades to a null capacity: the recorded facts still answer.
func (h *objectStorageHandlers) payload(r *http.Request, row store.ObjectStore) (objectStorePayload, error) {
	buckets, err := h.db.CountStoreAllocations(r.Context(), row.ID)
	if err != nil {
		return objectStorePayload{}, err
	}
	out := objectStorePayload{
		Name:          row.Name,
		State:         row.State,
		Image:         row.Image,
		Managed:       h.managed,
		Masters:       row.Masters,
		VolumeServers: row.VolumeServers,
		Replication:   row.Replication,
		VolumeSize: objectStoreVolumeSizePayload{
			LimitMB:   seaweed.VolumeSizeLimitMB(row.VolumeSizeLimitMb),
			Default:   row.VolumeSizeLimitMb == nil,
			DefaultMB: seaweed.DefaultVolumeSizeLimitMB,
			MinMB:     seaweed.MinVolumeSizeLimitMB,
			MaxMB:     seaweed.MaxVolumeSizeLimitMB,
		},
		Buckets:   buckets,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
	if h.storage != nil {
		slots, err := h.storage.ObjectStoreSlots(r.Context())
		if err != nil {
			slog.WarnContext(r.Context(), "read object store slots", "err", err)
		} else {
			out.Capacity = capacityPayload(slots, row.Replication, h.managed)
		}
	}
	if h.reconcile != nil {
		if status, source, ok := h.reconcile.ObjectStore(seaweed.SharedKey, seaweed.SourceName); ok {
			observed := &objectStoreObservedPayload{
				State:                     source.State,
				MastersReady:              status.MastersReady,
				VolumeServersReady:        status.VolumeServersReady,
				FilerReady:                status.FilerReady,
				S3Ready:                   status.S3Ready,
				S3Detail:                  status.S3Detail,
				UnderReplicatedVolumes:    status.UnderReplicatedVolumes,
				ReplicationPendingVolumes: status.ReplicationPendingVolumes,
				VolumeSizeLimitMB:         status.VolumeSizeLimitMB,
			}
			if source.State != module.SourceFresh && !source.StaleSince.IsZero() {
				since := source.StaleSince
				observed.StaleSince = &since
			}
			out.Observed = observed
		}
	}
	return out, nil
}

func capacityPayload(slots seaweed.SlotReport, replication string, managed bool) *objectStoreCapacityPayload {
	copies := seaweed.ReplicationCopies(replication)
	perBucket := seaweed.VolumesPerBucket(managed)
	out := &objectStoreCapacityPayload{
		Slots:            slots.Total(),
		UsedSlots:        slots.Used(),
		FreeSlots:        slots.Free(),
		VolumeHeadroom:   slots.VolumeHeadroom(copies),
		BucketHeadroom:   slots.BucketHeadroom(replication, perBucket),
		VolumesPerBucket: perBucket,
		Copies:           copies,
		Servers:          make([]objectStoreServerPayload, 0, len(slots.Servers)),
	}
	for _, server := range slots.Servers {
		out.Servers = append(out.Servers, objectStoreServerPayload{
			URL: server.URL, Slots: server.Max, UsedSlots: server.Volumes, FreeSlots: server.Free(),
		})
	}
	return out
}

// get serves the store with its volume size setting and live capacity.
func (h *objectStorageHandlers) get(w http.ResponseWriter, r *http.Request) {
	row, ok := h.liveStore(w, r)
	if !ok {
		return
	}
	payload, err := h.payload(r, *row)
	if err != nil {
		writeInternalError(r.Context(), w, "project object store", err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Store objectStorePayload `json:"store"`
	}{payload})
}

// putSettings changes the store's settings. volume_size_limit_mb null
// returns the limit to the default, a number sets it. The substrate
// re-renders the masters right away; they restart one at a time and the
// volume servers pick the new limit up from their next heartbeat.
func (h *objectStorageHandlers) putSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		VolumeSizeLimitMB json.RawMessage `json:"volume_size_limit_mb"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	// An absent field decodes to an empty raw message; an explicit null
	// arrives as the four bytes "null".
	if len(req.VolumeSizeLimitMB) == 0 {
		writeError(w, http.StatusBadRequest, codeBadRequest, "nothing to update: give volume_size_limit_mb")
		return
	}
	var limit *int32
	if string(req.VolumeSizeLimitMB) != "null" {
		var mb int
		if err := json.Unmarshal(req.VolumeSizeLimitMB, &mb); err != nil {
			writeError(w, http.StatusBadRequest, codeBadRequest, "volume_size_limit_mb must be an integer number of megabytes or null")
			return
		}
		if err := seaweed.ValidateVolumeSizeLimitMB(mb); err != nil {
			writeError(w, http.StatusUnprocessableEntity, codeBadRequest, err.Error())
			return
		}
		value := int32(mb)
		limit = &value
	}

	row, ok := h.liveStore(w, r)
	if !ok {
		return
	}
	if err := h.db.SetObjectStoreVolumeSizeLimit(r.Context(), row.ID, limit); err != nil {
		if errors.Is(err, dbstore.ErrNotFound) {
			writeError(w, http.StatusNotFound, codeNotFound, "no object store on this installation")
			return
		}
		writeInternalError(r.Context(), w, "save object store settings", err)
		return
	}
	logAccessChange(r, "object store settings changed", "object_store", row.Name,
		map[string]any{"volume_size_limit_mb": row.VolumeSizeLimitMb},
		map[string]any{"volume_size_limit_mb": limit})
	if h.storage != nil {
		h.storage.EnqueueObjectStoreSettings()
	}

	updated, ok := h.liveStore(w, r)
	if !ok {
		return
	}
	payload, err := h.payload(r, *updated)
	if err != nil {
		writeInternalError(r.Context(), w, "project object store", err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Store objectStorePayload `json:"store"`
	}{payload})
}
