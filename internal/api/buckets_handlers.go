package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/dbstore"
)

// bucketsHandlers serves bucket-service connection projections, mirroring
// the database pair. The connection endpoint reads durable rows only;
// credential reveal is the sanctioned request-time cluster read behind
// fresh authentication, returned once and never journaled or logged.
type bucketsHandlers struct {
	db      *dbstore.Service
	secrets func(ctx context.Context, namespace, name string) (map[string][]byte, error)
}

type bucketConnectionPayload struct {
	Service           string `json:"service"`
	Phase             string `json:"phase"`
	Visibility        string `json:"visibility"`
	StorageQuotaBytes int64  `json:"storage_quota_bytes,omitempty"`
	Endpoint          string `json:"endpoint,omitempty"`
	Bucket            string `json:"bucket,omitempty"`
	Region            string `json:"region,omitempty"`
	CredentialVersion int64  `json:"credential_version,omitempty"`
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
		payload.Bucket = allocation.BucketName
		payload.Region = allocation.Region
		payload.CredentialVersion = allocation.CredentialVersion
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
