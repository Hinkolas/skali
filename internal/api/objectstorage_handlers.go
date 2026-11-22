package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/metrics"
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
	// ObjectStoreCollections reads every bucket collection's footprint
	// from the master.
	ObjectStoreCollections(ctx context.Context) (map[string]seaweed.CollectionStat, error)
	// EnqueueObjectStoreSettings re-applies the store now instead of at
	// the next resync.
	EnqueueObjectStoreSettings()
}

type objectStorageHandlers struct {
	db      *dbstore.Service
	storage ObjectStorage
	// reconcile serves the provider observation; nil reports none.
	reconcile *reconcile.Kernel
	// metrics serves the nodes' newest free disk; nil leaves it out.
	metrics *metrics.Service
	managed bool
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

// objectStoreServerPayload is one volume server's slots, with the inputs
// of an estimate under another volume size limit: a server offers its
// volumes plus (free disk - the room its writable volumes may still grow
// into) / limit. free_disk_bytes is its node's newest sampled free disk,
// null when unknown; writable_volume_bytes null when unread.
type objectStoreServerPayload struct {
	URL                 string  `json:"url"`
	Node                string  `json:"node,omitempty"`
	Slots               int     `json:"slots"`
	UsedSlots           int     `json:"used_slots"`
	FreeSlots           int     `json:"free_slots"`
	FreeDiskBytes       *int64  `json:"free_disk_bytes"`
	WritableVolumeBytes []int64 `json:"writable_volume_bytes"`
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
			out.Capacity = capacityPayload(slots, row.Replication, h.managed, h.freeDisk(r))
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

// freeDisk maps node names to their newest sampled free disk; empty when
// no sampler runs or the read fails.
func (h *objectStorageHandlers) freeDisk(r *http.Request) map[string]int64 {
	free := map[string]int64{}
	if h.metrics == nil {
		return free
	}
	nodes, err := h.metrics.NodesStorage(r.Context(), time.Now())
	if err != nil {
		slog.WarnContext(r.Context(), "read node storage", "err", err)
		return free
	}
	for _, node := range nodes {
		free[node.Name] = node.AvailableBytes
	}
	return free
}

func capacityPayload(slots seaweed.SlotReport, replication string, managed bool, freeDisk map[string]int64) *objectStoreCapacityPayload {
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
		entry := objectStoreServerPayload{
			URL: server.URL, Node: server.Node, Slots: server.Max, UsedSlots: server.Volumes, FreeSlots: server.Free(),
			WritableVolumeBytes: server.WritableVolumeBytes,
		}
		if free, ok := freeDisk[server.Node]; ok && server.Node != "" {
			entry.FreeDiskBytes = &free
		}
		out.Servers = append(out.Servers, entry)
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

// objectStoreUsagePayload is one collection's footprint on the volume
// servers. used_bytes is what the quota is judged on (deleted objects no
// longer count), disk_bytes what the volumes hold until the vacuum
// reclaims the deleted bytes, both counted once however many copies
// exist. volumes are the collection's distinct volumes, slots their
// copies: the volume slots it holds.
type objectStoreUsagePayload struct {
	UsedBytes    int64 `json:"used_bytes"`
	DiskBytes    int64 `json:"disk_bytes"`
	DeletedBytes int64 `json:"deleted_bytes"`
	EntryCount   int64 `json:"entry_count"`
	Volumes      int   `json:"volumes"`
	Slots        int   `json:"slots"`
}

func usagePayload(stat seaweed.CollectionStat) *objectStoreUsagePayload {
	return &objectStoreUsagePayload{
		UsedBytes:    stat.LiveBytes,
		DiskBytes:    stat.SizeBytes,
		DeletedBytes: stat.DeletedBytes,
		EntryCount:   stat.EntryCount,
		Volumes:      stat.Volumes,
		Slots:        stat.Replicas,
	}
}

// objectStoreBucketPayload is one live bucket with its owner. Project and
// environment are null for system-owned buckets; usage is null when the
// master could not be read. A bucket that has not been written to holds
// no volume yet and reports zero usage.
type objectStoreBucketPayload struct {
	BucketName  string                   `json:"bucket_name"`
	Owner       string                   `json:"owner"`
	SystemKey   string                   `json:"system_key,omitempty"`
	ServiceKey  string                   `json:"service_key,omitempty"`
	Project     *poolProjectRef          `json:"project"`
	Environment *poolEnvironmentRef      `json:"environment"`
	Phase       string                   `json:"phase"`
	Visibility  string                   `json:"visibility"`
	QuotaBytes  int64                    `json:"quota_bytes"`
	Usage       *objectStoreUsagePayload `json:"usage"`
	CreatedAt   time.Time                `json:"created_at"`
}

// objectStoreCollectionPayload is a collection on the volume servers that
// no live bucket claims: what a deleted bucket left behind (its teardown
// had not dropped the collection) or a bucket created outside skali. It
// still holds its slots.
type objectStoreCollectionPayload struct {
	Collection string                  `json:"collection"`
	Usage      objectStoreUsagePayload `json:"usage"`
}

type objectStoreBucketsPayload struct {
	Buckets   []objectStoreBucketPayload     `json:"buckets"`
	Unclaimed []objectStoreCollectionPayload `json:"unclaimed"`
	// UsageObserved is false when the master could not be read: every
	// usage is null and unclaimed collections are unknown.
	UsageObserved bool `json:"usage_observed"`
}

// buckets serves every live bucket on the store with its owner and live
// footprint, plus the collections no live bucket claims.
func (h *objectStorageHandlers) buckets(w http.ResponseWriter, r *http.Request) {
	row, ok := h.liveStore(w, r)
	if !ok {
		return
	}
	rows, err := h.db.ListStoreBucketDetails(r.Context(), row.ID)
	if err != nil {
		writeInternalError(r.Context(), w, "list object store buckets", err)
		return
	}
	var stats map[string]seaweed.CollectionStat
	if h.storage != nil {
		stats, err = h.storage.ObjectStoreCollections(r.Context())
		if err != nil {
			slog.WarnContext(r.Context(), "read object store collections", "err", err)
			stats = nil
		}
	}
	payload := objectStoreBucketsPayload{
		Buckets:       make([]objectStoreBucketPayload, 0, len(rows)),
		Unclaimed:     []objectStoreCollectionPayload{},
		UsageObserved: stats != nil,
	}
	claimed := make(map[string]bool, len(rows))
	for _, bucket := range rows {
		claimed[bucket.BucketName] = true
		entry := objectStoreBucketPayload{
			BucketName: bucket.BucketName,
			Owner:      bucket.OwnerKind,
			SystemKey:  bucket.SystemKey,
			ServiceKey: bucket.ServiceKey,
			Phase:      bucket.Phase,
			Visibility: bucket.Visibility,
			QuotaBytes: bucket.StorageQuotaBytes,
			CreatedAt:  bucket.CreatedAt,
		}
		if stats != nil {
			entry.Usage = usagePayload(stats[bucket.BucketName])
		}
		if bucket.ProjectID != nil && bucket.ProjectName != nil {
			entry.Project = &poolProjectRef{ID: bucket.ProjectID.String(), Name: *bucket.ProjectName}
			if bucket.ProjectDisplayName != nil {
				entry.Project.DisplayName = *bucket.ProjectDisplayName
			}
		}
		if bucket.EnvironmentID != nil && bucket.EnvironmentName != nil {
			entry.Environment = &poolEnvironmentRef{ID: bucket.EnvironmentID.String(), Name: *bucket.EnvironmentName}
		}
		payload.Buckets = append(payload.Buckets, entry)
	}
	for collection, stat := range stats {
		if claimed[collection] {
			continue
		}
		payload.Unclaimed = append(payload.Unclaimed, objectStoreCollectionPayload{
			Collection: collection, Usage: *usagePayload(stat),
		})
	}
	slices.SortFunc(payload.Unclaimed, func(a, b objectStoreCollectionPayload) int {
		if a.Usage.Slots != b.Usage.Slots {
			return b.Usage.Slots - a.Usage.Slots
		}
		return strings.Compare(a.Collection, b.Collection)
	})
	writeJSON(w, http.StatusOK, payload)
}
