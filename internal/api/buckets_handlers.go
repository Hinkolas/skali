package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/rotation"
	"github.com/Hinkolas/skali/internal/substrate"
)

// Bounds of a rotation's overlap window: below a minute the previous key
// would be revoked before any consumer rolled; above seven days nothing
// legitimately holds it (the SigV4 presign maximum).
const (
	defaultRetireAfter = time.Hour
	minRetireAfter     = time.Minute
	maxRetireAfter     = 7 * 24 * time.Hour
)

// bucketsHandlers serves bucket-service connection projections, mirroring
// the database pair. The connection endpoint reads durable rows only;
// credential reveal is the sanctioned request-time cluster read behind
// fresh authentication, returned once and never journaled or logged.
type bucketsHandlers struct {
	db      *dbstore.Service
	secrets func(ctx context.Context, namespace, name string) (map[string][]byte, error)
	// rotation accepts credential rotations; nil hides the route.
	rotation *rotation.Controller
}

type bucketConnectionPayload struct {
	Service           string `json:"service"`
	Phase             string `json:"phase"`
	Visibility        string `json:"visibility"`
	StorageQuotaBytes int64  `json:"storage_quota_bytes,omitempty"`
	Endpoint          string `json:"endpoint,omitempty"`
	InternalEndpoint  string `json:"internal_endpoint,omitempty"`
	Bucket            string `json:"bucket,omitempty"`
	Region            string `json:"region,omitempty"`
	CredentialVersion int64  `json:"credential_version,omitempty"`
	// CredentialRetireAt is set while a rotation's previous keypair is
	// still accepted: the instant it retires.
	CredentialRetireAt *time.Time `json:"credential_retire_at,omitempty"`
}

type bucketCredentialsPayload struct {
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
}

func (h *bucketsHandlers) connection(w http.ResponseWriter, r *http.Request) {
	environmentID, ok := pathID(w, r)
	if !ok {
		return
	}
	key := chi.URLParam(r, "key")
	row, err := h.db.LiveServiceBucketClaim(r.Context(), environmentID, key)
	if err != nil {
		if errors.Is(err, dbstore.ErrNotFound) {
			writeError(w, http.StatusNotFound, codeNotFound, "no live bucket claim for "+key)
			return
		}
		writeError(w, http.StatusInternalServerError, codeInternal, "loading the claim failed")
		return
	}
	payload := bucketConnectionPayload{
		Service:           "buckets." + key,
		Phase:             row.Phase,
		Visibility:        row.Visibility,
		StorageQuotaBytes: row.StorageQuotaBytes,
	}
	if allocation, err := h.db.LiveAllocation(r.Context(), row.ID); err == nil {
		payload.Endpoint = allocation.Endpoint
		payload.InternalEndpoint = substrate.InternalBucketEndpoint()
		payload.Bucket = allocation.BucketName
		payload.Region = allocation.Region
		payload.CredentialVersion = allocation.CredentialVersion
		payload.CredentialRetireAt = allocation.CredentialRetireAt
	}
	writeJSON(w, http.StatusOK, payload)
}

func (h *bucketsHandlers) reveal(w http.ResponseWriter, r *http.Request) {
	environmentID, ok := pathID(w, r)
	if !ok {
		return
	}
	if h.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, codeInternal, "no cluster available for credential reveal")
		return
	}
	key := chi.URLParam(r, "key")
	row, err := h.db.LiveServiceBucketClaim(r.Context(), environmentID, key)
	if err != nil {
		if errors.Is(err, dbstore.ErrNotFound) {
			writeError(w, http.StatusNotFound, codeNotFound, "no live bucket claim for "+key)
			return
		}
		writeError(w, http.StatusInternalServerError, codeInternal, "loading the claim failed")
		return
	}
	if claim.Phase(row.Phase) != claim.PhaseProvisioned {
		writeError(w, http.StatusConflict, codeConflict, "the bucket is not provisioned yet")
		return
	}
	allocation, err := h.db.LiveAllocation(r.Context(), row.ID)
	if err != nil {
		writeError(w, http.StatusConflict, codeConflict, "the bucket has no live allocation")
		return
	}
	data, err := h.secrets(r.Context(), "skali-platform", allocation.CredentialSecret)
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "reading the credential failed")
		return
	}
	writeJSON(w, http.StatusOK, bucketCredentialsPayload{
		AccessKey: string(data["access_key"]),
		SecretKey: string(data["secret_key"]),
	})
}

// POST /v1/environments/{id}/buckets/{key}/credentials/rotate: issue a new
// keypair as a journaled run of kind rotation. The consumers roll onto it
// and the previous keypair retires after the overlap window; URLs signed
// with it fail from then on, so the route sits behind sudo mode.
func (h *bucketsHandlers) rotate(w http.ResponseWriter, r *http.Request) {
	environmentID, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		RetireAfterSeconds *int64 `json:"retire_after_seconds"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSON(w, r, &req); err != nil {
			writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
			return
		}
	}
	retireAfter := defaultRetireAfter
	if req.RetireAfterSeconds != nil {
		retireAfter = time.Duration(*req.RetireAfterSeconds) * time.Second
		if retireAfter < minRetireAfter || retireAfter > maxRetireAfter {
			writeError(w, http.StatusBadRequest, codeBadRequest,
				"retire_after_seconds must be between 60 (one minute) and 604800 (seven days)")
			return
		}
	}
	runID, err := h.rotation.Create(r.Context(), rotation.Input{
		EnvironmentID: environmentID,
		ServiceKey:    chi.URLParam(r, "key"),
		RetireAfter:   retireAfter,
		Actor:         UserFrom(r.Context()).ID.String(),
	})
	if err != nil {
		writeRotationError(w, chi.URLParam(r, "key"), err)
		return
	}
	writeJSON(w, http.StatusAccepted, struct {
		RunID string `json:"run_id"`
	}{runID.String()})
}

func writeRotationError(w http.ResponseWriter, key string, err error) {
	switch {
	case errors.Is(err, rotation.ErrEnvironmentNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "environment not found")
	case errors.Is(err, rotation.ErrBucketNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "no live bucket claim for "+key)
	case errors.Is(err, substrate.ErrBucketNotProvisioned):
		writeError(w, http.StatusConflict, codeBucketNotProvisioned, "the bucket is not provisioned yet")
	case errors.Is(err, substrate.ErrBucketFenced):
		writeError(w, http.StatusConflict, codeBucketFenced,
			"a restore holds the bucket; rotate once it has finished")
	case errors.Is(err, rotation.ErrRunInFlight):
		writeError(w, http.StatusConflict, codeRunInFlight,
			"another run is in flight for this environment; wait for it or cancel it")
	default:
		writeError(w, http.StatusInternalServerError, codeInternal, "starting the rotation failed")
	}
}
