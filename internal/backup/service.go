package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/store"
)

// Backup row kinds and statuses (mirrors the 00016 CHECK constraints).
const (
	KindBackup  = "backup"
	KindRestore = "restore"

	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
)

var (
	ErrEnvironmentNotFound  = errors.New("backup: environment not found")
	ErrEnvironmentNotActive = errors.New("backup: environment is not active")
	// ErrBackupInFlight: the environment already has a running run
	// (deployment, backup, or otherwise); the journal's one-running-run
	// index is the arbiter.
	ErrBackupInFlight = errors.New("backup: another run is in flight for this environment")
)

// TargetUnreachableError wraps a synchronous S3 failure so the API can
// answer 503 instead of an opaque 500.
type TargetUnreachableError struct{ Err error }

func (e *TargetUnreachableError) Error() string {
	return fmt.Sprintf("backup: target unreachable: %v", e.Err)
}

func (e *TargetUnreachableError) Unwrap() error { return e.Err }

// CreateResult identifies the accepted operation.
type CreateResult struct {
	BackupID uuid.UUID
	RunID    uuid.UUID
}

// CreateBackup accepts a manual backup of one environment: it validates the
// environment is active, claims the environment's single running-run slot
// with a run of kind "backup", inserts the driving row, and enqueues
// execution. 202 semantics: the work happens in the controller.
func (c *Controller) CreateBackup(ctx context.Context, environmentID uuid.UUID, actor string) (*CreateResult, error) {
	names, err := c.environmentNames(ctx, environmentID)
	if err != nil {
		return nil, err
	}
	target, err := c.deps.Store.GetEnvironmentTarget(ctx, environmentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrEnvironmentNotFound
		}
		return nil, fmt.Errorf("backup: get environment target: %w", err)
	}
	if target.State != "active" || target.ActiveRevisionID == nil {
		return nil, ErrEnvironmentNotActive
	}
	// A configured target is a precondition; reachability is proven inside
	// the run where the failure has a visible step.
	if _, err := c.deps.Targets.Get(ctx, DefaultTargetName); err != nil {
		return nil, err
	}

	run, err := c.deps.Journal.CreateRun(ctx, journal.RunInput{
		Kind:          KindBackup,
		ProjectID:     names.projectID,
		EnvironmentID: environmentID,
		Actor:         actor,
	})
	if err != nil {
		return nil, fmt.Errorf("backup: create run: %w", err)
	}
	if err := c.deps.Journal.StartRun(ctx, run.ID); err != nil {
		if discardErr := c.deps.Journal.DiscardRun(ctx, run.ID); discardErr != nil {
			slog.WarnContext(ctx, "backup: discard unstarted run", "run", run.ID, "err", discardErr)
		}
		if errors.Is(err, journal.ErrRunConflict) {
			return nil, ErrBackupInFlight
		}
		return nil, fmt.Errorf("backup: start run: %w", err)
	}

	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("backup: generate id: %w", err)
	}
	runID := run.ID
	row, err := c.deps.Store.CreateBackup(ctx, store.CreateBackupParams{
		ID:              id,
		Kind:            KindBackup,
		EnvironmentID:   environmentID,
		ProjectName:     names.project,
		EnvironmentName: names.environment,
		RevisionID:      target.ActiveRevisionID,
		RunID:           &runID,
	})
	if err != nil {
		_ = c.deps.Journal.FinishRun(ctx, run.ID, journal.RunFailed)
		return nil, fmt.Errorf("backup: create row: %w", err)
	}
	c.Enqueue(row.ID)
	return &CreateResult{BackupID: row.ID, RunID: run.ID}, nil
}

// RecoverOnBoot fails every backup row a dead daemon left unfinished. The
// journal's own recovery already failed the orphaned attempts; this closes
// the rows and runs and sweeps leftover Jobs. Backups are not resumable by
// design: re-running one is cheap and unambiguous.
func (c *Controller) RecoverOnBoot(ctx context.Context) error {
	rows, err := c.deps.Store.ListUnfinishedBackups(ctx)
	if err != nil {
		return fmt.Errorf("backup: list unfinished: %w", err)
	}
	for _, row := range rows {
		message := row.Kind + " interrupted: the daemon restarted while it was running"
		if row.RunID != nil {
			if err := c.deps.Journal.FinishRun(ctx, *row.RunID, journal.RunFailed); err != nil &&
				!errors.Is(err, journal.ErrInvalidTransition) && !errors.Is(err, journal.ErrNotFound) {
				slog.WarnContext(ctx, "backup: finish interrupted run", "run", *row.RunID, "err", err)
			}
		}
		if _, err := c.deps.Store.SetBackupStatus(ctx, store.SetBackupStatusParams{
			ID:         row.ID,
			ToStatus:   StatusFailed,
			FromStatus: row.Status,
			Error:      &message,
		}); err != nil {
			slog.WarnContext(ctx, "backup: fail interrupted row", "backup", row.ID, "err", err)
		}
		if c.deps.Kube != nil {
			c.cleanupJobs(ctx, &row)
		}
	}
	return nil
}

// SnapshotSummary is one listable snapshot, read from its S3 manifest.
type SnapshotSummary struct {
	ID               string    `json:"id"`
	CreatedAt        time.Time `json:"created_at"`
	RevisionChecksum string    `json:"revision_checksum"`
	Encryption       string    `json:"encryption"`
	Databases        int       `json:"databases"`
	Buckets          int       `json:"buckets"`
	Volumes          int       `json:"volumes"`
	Bytes            int64     `json:"bytes"`
}

func summarize(m *Manifest) SnapshotSummary {
	summary := SnapshotSummary{
		ID:               m.SnapshotID,
		CreatedAt:        m.CreatedAt,
		RevisionChecksum: m.RevisionChecksum,
		Encryption:       m.Encryption,
	}
	for _, component := range m.Components {
		summary.Bytes += component.Bytes
		switch component.Kind {
		case ComponentDatabase:
			summary.Databases++
		case ComponentBucket:
			summary.Buckets++
		case ComponentVolume:
			summary.Volumes++
		}
	}
	return summary
}

// ListSnapshots enumerates the environment's snapshots from S3, newest
// first. The control-plane database is deliberately not consulted: after a
// reinstall it knows nothing, and the bucket is the truth.
func (c *Controller) ListSnapshots(ctx context.Context, project, environment string) ([]SnapshotSummary, error) {
	credentials, err := c.deps.Targets.credentials(ctx, DefaultTargetName)
	if err != nil {
		return nil, err
	}
	target, err := newObjectStore(targetLocation(credentials))
	if err != nil {
		return nil, err
	}
	if err := target.Reachable(ctx); err != nil {
		return nil, &TargetUnreachableError{Err: err}
	}
	var keys []string
	err = target.List(ctx, snapshotPrefix(credentials.Prefix, project, environment), func(info objectInfo) error {
		keys = append(keys, info.Key)
		return nil
	})
	if err != nil {
		return nil, &TargetUnreachableError{Err: err}
	}
	summaries := make([]SnapshotSummary, 0, len(keys))
	for _, key := range keys {
		manifest, err := c.readManifest(ctx, target, key)
		if err != nil {
			var unsupported *UnsupportedManifestError
			if errors.As(err, &unsupported) {
				slog.WarnContext(ctx, "backup: skip unsupported manifest", "key", key, "err", err)
				continue
			}
			return nil, err
		}
		summaries = append(summaries, summarize(manifest))
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].CreatedAt.After(summaries[j].CreatedAt)
	})
	return summaries, nil
}

func (c *Controller) readManifest(ctx context.Context, target objectStore, key string) (*Manifest, error) {
	reader, err := target.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	data, err := readAll(reader, maxManifestBytes)
	if err != nil {
		return nil, fmt.Errorf("backup: read manifest %s: %w", key, err)
	}
	return decodeManifest(data)
}

type environmentNames struct {
	projectID   uuid.UUID
	project     string
	environment string
}

func (c *Controller) environmentNames(ctx context.Context, environmentID uuid.UUID) (*environmentNames, error) {
	environment, err := c.deps.Store.GetEnvironmentByID(ctx, environmentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrEnvironmentNotFound
		}
		return nil, fmt.Errorf("backup: get environment: %w", err)
	}
	project, err := c.deps.Store.GetProjectByID(ctx, environment.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("backup: get project: %w", err)
	}
	return &environmentNames{
		projectID:   project.ID,
		project:     project.Name,
		environment: environment.Name,
	}, nil
}
