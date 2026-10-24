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
	// backups summarizes the target's contents; nil (API-only mode) hides
	// the summary route.
	backups *backup.Controller
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

type backupSummaryPayload struct {
	Target            backupTargetPayload    `json:"target"`
	Reachable         bool                   `json:"reachable"`
	UnreachableReason string                 `json:"unreachable_reason,omitempty"`
	CheckedAt         time.Time              `json:"checked_at"`
	Snapshots         *snapshotTotalsPayload `json:"snapshots"`
	Runs              runTotalsPayload       `json:"runs"`
	Schedules         scheduleTotalsPayload  `json:"schedules"`
}

type snapshotTotalsPayload struct {
	Count     int                    `json:"count"`
	Bytes     int64                  `json:"bytes"`
	Manual    int                    `json:"manual"`
	Scheduled int                    `json:"scheduled"`
	Orphaned  int                    `json:"orphaned"`
	OldestAt  *time.Time             `json:"oldest_at"`
	NewestAt  *time.Time             `json:"newest_at"`
	Projects  []projectTotalsPayload `json:"projects"`
}

type projectTotalsPayload struct {
	Project  string    `json:"project"`
	Count    int       `json:"count"`
	Bytes    int64     `json:"bytes"`
	NewestAt time.Time `json:"newest_at"`
}

type runTotalsPayload struct {
	Since         time.Time       `json:"since"`
	Succeeded     int64           `json:"succeeded"`
	Failed        int64           `json:"failed"`
	BackingUp     int64           `json:"backing_up"`
	Restoring     int64           `json:"restoring"`
	LastSucceeded *runInfoPayload `json:"last_succeeded"`
	LastFailed    *runInfoPayload `json:"last_failed"`
}

type runInfoPayload struct {
	Project     string    `json:"project"`
	Environment string    `json:"environment"`
	FinishedAt  time.Time `json:"finished_at"`
	Error       string    `json:"error,omitempty"`
}

type scheduleTotalsPayload struct {
	Environments int64 `json:"environments"`
	Scheduled    int64 `json:"scheduled"`
}

func newRunInfoPayload(info *backup.RunInfo) *runInfoPayload {
	if info == nil {
		return nil
	}
	return &runInfoPayload{
		Project: info.Project, Environment: info.Environment,
		FinishedAt: info.FinishedAt, Error: info.Error,
	}
}

// GET /v1/system/backup-target/summary: the target with what its bucket
// holds and how backups have been running.
func (h *backupTargetHandlers) summary(w http.ResponseWriter, r *http.Request) {
	target, err := h.targets.Get(r.Context(), backup.DefaultTargetName)
	if err != nil {
		if errors.Is(err, backup.ErrTargetNotFound) {
			writeError(w, http.StatusNotFound, codeNotFound, "backup target not configured")
			return
		}
		writeInternalError(r.Context(), w, "get backup target", err)
		return
	}
	summary, err := h.backups.Summarize(r.Context())
	if err != nil {
		if errors.Is(err, backup.ErrTargetNotFound) {
			writeError(w, http.StatusNotFound, codeNotFound, "backup target not configured")
			return
		}
		writeInternalError(r.Context(), w, "summarize backup target", err)
		return
	}
	payload := backupSummaryPayload{
		Target:            newBackupTargetPayload(target),
		Reachable:         summary.Reachable,
		UnreachableReason: summary.UnreachableReason,
		CheckedAt:         summary.CheckedAt,
		Runs: runTotalsPayload{
			Since: summary.Runs.Since, Succeeded: summary.Runs.Succeeded, Failed: summary.Runs.Failed,
			BackingUp: summary.Runs.BackingUp, Restoring: summary.Runs.Restoring,
			LastSucceeded: newRunInfoPayload(summary.Runs.LastSucceeded),
			LastFailed:    newRunInfoPayload(summary.Runs.LastFailed),
		},
		Schedules: scheduleTotalsPayload{
			Environments: summary.Schedules.Environments, Scheduled: summary.Schedules.Scheduled,
		},
	}
	if totals := summary.Snapshots; totals != nil {
		projects := make([]projectTotalsPayload, 0, len(totals.Projects))
		for _, project := range totals.Projects {
			projects = append(projects, projectTotalsPayload{
				Project: project.Project, Count: project.Count, Bytes: project.Bytes, NewestAt: project.NewestAt,
			})
		}
		payload.Snapshots = &snapshotTotalsPayload{
			Count: totals.Count, Bytes: totals.Bytes, Manual: totals.Manual, Scheduled: totals.Scheduled,
			Orphaned: totals.Orphaned, OldestAt: totals.OldestAt, NewestAt: totals.NewestAt, Projects: projects,
		}
	}
	writeJSON(w, http.StatusOK, payload)
}
