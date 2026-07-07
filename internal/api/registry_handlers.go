package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/mirror"
	"github.com/Hinkolas/skali/internal/store"
)

// importImageTimeout bounds an import end-to-end. Generous: a large
// multi-arch image crosses the WAN once here so every later node pull is a
// LAN hit.
const importImageTimeout = 15 * time.Minute

// RegistryOps is the mirror surface the handlers consume; *mirror.Importer
// implements it. An interface so API tests stub the registry away and so a
// registry-less deployment (no CLUSTER_ADDR) keeps the routes — they answer
// 503 registry_disabled instead of vanishing.
type RegistryOps interface {
	List(ctx context.Context) ([]store.RegistryImage, error)
	Import(ctx context.Context, reference string) (store.RegistryImage, error)
	Delete(ctx context.Context, id uuid.UUID) error
}

// registryHandlers is the admin-only mirror catalog: what the cluster
// registry serves, imports into it, deletes from it.
type registryHandlers struct {
	registry RegistryOps // nil = registry disabled
}

type registryImagePayload struct {
	ID         uuid.UUID `json:"id"`
	Repository string    `json:"repository"`
	Tag        string    `json:"tag"`
	Digest     string    `json:"digest"`
	SizeBytes  int64     `json:"size_bytes"`
	ImportedAt time.Time `json:"imported_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func newRegistryImagePayload(r *store.RegistryImage) registryImagePayload {
	return registryImagePayload{
		ID:         r.ID,
		Repository: r.Repository,
		Tag:        r.Tag,
		Digest:     r.Digest,
		SizeBytes:  r.SizeBytes,
		ImportedAt: r.ImportedAt,
		UpdatedAt:  r.UpdatedAt,
	}
}

// enabled gates every handler: nil ops means the master runs no registry
// (CLUSTER_ADDR unset).
func (h *registryHandlers) enabled(w http.ResponseWriter) bool {
	if h.registry == nil {
		writeError(w, http.StatusServiceUnavailable, codeRegistryDisabled,
			"the image mirror is disabled: set CLUSTER_ADDR on the master")
		return false
	}
	return true
}

func (h *registryHandlers) list(w http.ResponseWriter, r *http.Request) {
	if !h.enabled(w) {
		return
	}
	rows, err := h.registry.List(r.Context())
	if err != nil {
		writeRegistryError(r.Context(), w, err)
		return
	}
	images := make([]registryImagePayload, 0, len(rows))
	for i := range rows {
		images = append(images, newRegistryImagePayload(&rows[i]))
	}
	writeJSON(w, http.StatusOK, struct {
		Images []registryImagePayload `json:"images"`
	}{images})
}

func (h *registryHandlers) importImage(w http.ResponseWriter, r *http.Request) {
	if !h.enabled(w) {
		return
	}
	var req struct {
		Reference string `json:"reference"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	if req.Reference == "" {
		writeError(w, http.StatusBadRequest, codeBadRequest, "reference is required")
		return
	}

	// Detached from the router's 30s ceiling like container pulls: the copy
	// finishes server-side even when the client gives up waiting.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), importImageTimeout)
	defer cancel()
	row, err := h.registry.Import(ctx, req.Reference)
	if err != nil {
		writeRegistryError(r.Context(), w, err)
		return
	}
	slog.InfoContext(r.Context(), "api: image imported",
		"reference", req.Reference, "digest", row.Digest, "by", UserFrom(r.Context()).ID)
	writeJSON(w, http.StatusCreated, struct {
		Image registryImagePayload `json:"image"`
	}{newRegistryImagePayload(&row)})
}

func (h *registryHandlers) deleteImage(w http.ResponseWriter, r *http.Request) {
	if !h.enabled(w) {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return
	}
	if err := h.registry.Delete(r.Context(), id); err != nil {
		writeRegistryError(r.Context(), w, err)
		return
	}
	slog.InfoContext(r.Context(), "api: image removed from mirror",
		"id", id, "by", UserFrom(r.Context()).ID)
	w.WriteHeader(http.StatusNoContent)
}

// writeRegistryError maps mirror sentinels onto the envelope.
func writeRegistryError(ctx context.Context, w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, mirror.ErrInvalidReference):
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
	case errors.Is(err, mirror.ErrUpstreamNotFound), errors.Is(err, mirror.ErrImageNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, err.Error())
	case errors.Is(err, mirror.ErrRegistryUnavailable):
		writeError(w, http.StatusServiceUnavailable, codeRegistryUnavailable,
			"the cluster registry is unreachable (it may still be starting)")
	default:
		slog.ErrorContext(ctx, "api: registry error", "err", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
	}
}
