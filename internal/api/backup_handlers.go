package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/Hinkolas/skali/internal/backup"
)

// backupTargetHandlers manages the installation's backup locations. The
// secret access key is write-only: PUT accepts it, GET never returns it.
type backupTargetHandlers struct {
	targets *backup.TargetStore
}

type backupTargetPayload struct {
	Name        string    `json:"name"`
	Endpoint    string    `json:"endpoint"`
	Region      string    `json:"region"`
	Bucket      string    `json:"bucket"`
	Prefix      string    `json:"prefix"`
	AccessKeyID string    `json:"access_key_id"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func newBackupTargetPayload(t *backup.Target) backupTargetPayload {
	return backupTargetPayload{
		Name:        t.Name,
		Endpoint:    t.Endpoint,
		Region:      t.Region,
		Bucket:      t.Bucket,
		Prefix:      t.Prefix,
		AccessKeyID: t.AccessKeyID,
		CreatedAt:   t.CreatedAt,
		UpdatedAt:   t.UpdatedAt,
	}
}

func (h *backupTargetHandlers) get(w http.ResponseWriter, r *http.Request) {
	target, err := h.targets.Get(r.Context(), backup.DefaultTargetName)
	if err != nil {
		if errors.Is(err, backup.ErrTargetNotFound) {
			writeError(w, http.StatusNotFound, codeNotFound, "backup target not configured")
			return
		}
		writeInternalError(r.Context(), w, "get backup target", err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Target backupTargetPayload `json:"target"`
	}{newBackupTargetPayload(target)})
}

func (h *backupTargetHandlers) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.targets.Delete(r.Context(), backup.DefaultTargetName); err != nil {
		if errors.Is(err, backup.ErrTargetNotFound) {
			writeError(w, http.StatusNotFound, codeNotFound, "backup target not configured")
			return
		}
		writeInternalError(r.Context(), w, "delete backup target", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *backupTargetHandlers) put(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Endpoint        string `json:"endpoint"`
		Region          string `json:"region"`
		Bucket          string `json:"bucket"`
		Prefix          string `json:"prefix"`
		AccessKeyID     string `json:"access_key_id"`
		SecretAccessKey string `json:"secret_access_key"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	target, err := h.targets.Upsert(r.Context(), backup.TargetInput{
		Name:            backup.DefaultTargetName,
		Endpoint:        req.Endpoint,
		Region:          req.Region,
		Bucket:          req.Bucket,
		Prefix:          req.Prefix,
		AccessKeyID:     req.AccessKeyID,
		SecretAccessKey: req.SecretAccessKey,
	})
	if err != nil {
		var invalid *backup.ValidationError
		if errors.As(err, &invalid) {
			writeError(w, http.StatusUnprocessableEntity, codeBadRequest, invalid.Field+" "+invalid.Message)
			return
		}
		writeInternalError(r.Context(), w, "put backup target", err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Target backupTargetPayload `json:"target"`
	}{newBackupTargetPayload(target)})
}
