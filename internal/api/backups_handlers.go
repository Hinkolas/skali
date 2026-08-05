package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/backup"
	"github.com/Hinkolas/skali/internal/store"
)

// backupsHandlers accepts backup and restore operations and lists
// snapshots. Listing reads the S3 manifests, never the control-plane
// database: after a reinstall the bucket is the only truth.
type backupsHandlers struct {
	backups *backup.Controller
	st      *store.Store
}

// POST /v1/environments/{id}/backups: accept a manual snapshot; the
// controller does the work. 202 mirrors teardown.
func (h *backupsHandlers) create(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	user := UserFrom(r.Context())
	result, err := h.backups.CreateBackup(r.Context(), id, user.ID.String())
	if err != nil {
		writeBackupError(r.Context(), w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, struct {
		RunID    string `json:"run_id"`
		BackupID string `json:"backup_id"`
	}{result.RunID.String(), result.BackupID.String()})
}

// GET /v1/environments/{id}/backups: the environment's snapshots, newest
// first, read from the backup target.
func (h *backupsHandlers) list(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	environment, err := h.st.GetEnvironmentByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, codeNotFound, "environment not found")
			return
		}
		writeInternalError(r.Context(), w, "get environment", err)
		return
	}
	project, err := h.st.GetProjectByID(r.Context(), environment.ProjectID)
	if err != nil {
		writeInternalError(r.Context(), w, "get project", err)
		return
	}
	snapshots, err := h.backups.ListSnapshots(r.Context(), project.Name, environment.Name)
	if err != nil {
		writeBackupError(r.Context(), w, err)
		return
	}
	if snapshots == nil {
		snapshots = []backup.SnapshotSummary{}
	}
	writeJSON(w, http.StatusOK, struct {
		Snapshots []backup.SnapshotSummary `json:"snapshots"`
	}{snapshots})
}

// POST /v1/environments/{id}/restore: accept a stop-first restore of one
// snapshot. Destructive (current data is replaced), so the route sits
// behind sudo mode.
func (h *backupsHandlers) restore(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		SnapshotID string `json:"snapshot_id"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	if req.SnapshotID == "" {
		writeError(w, http.StatusBadRequest, codeBadRequest, "snapshot_id is required")
		return
	}
	user := UserFrom(r.Context())
	result, err := h.backups.CreateRestore(r.Context(), id, req.SnapshotID, user.ID.String())
	if err != nil {
		writeBackupError(r.Context(), w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, struct {
		RunID string `json:"run_id"`
	}{result.RunID.String()})
}

func writeBackupError(ctx context.Context, w http.ResponseWriter, err error) {
	var unreachable *backup.TargetUnreachableError
	switch {
	case errors.Is(err, backup.ErrEnvironmentNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "environment not found")
	case errors.Is(err, backup.ErrSnapshotNotFound):
		writeError(w, http.StatusNotFound, codeSnapshotNotFound,
			"no snapshot with this id exists on the backup target")
	case errors.Is(err, backup.ErrEnvironmentNotActive):
		writeError(w, http.StatusUnprocessableEntity, codeEnvironmentNotActive,
			"the environment is not active; deploy it before backing it up")
	case errors.Is(err, backup.ErrBackupInFlight):
		writeError(w, http.StatusConflict, codeBackupInFlight,
			"another run is in flight for this environment; wait for it or cancel it")
	case errors.Is(err, backup.ErrTargetNotFound):
		writeError(w, http.StatusServiceUnavailable, codeBackupTargetUnconfigured,
			"no backup target is configured; an admin must set one first")
	case errors.As(err, &unreachable):
		writeError(w, http.StatusServiceUnavailable, codeBackupTargetUnreachable,
			"the backup target did not answer: "+unreachable.Err.Error())
	default:
		writeInternalError(ctx, w, "backup", err)
	}
}
