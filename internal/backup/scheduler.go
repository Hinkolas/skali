package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/cron"
	"github.com/Hinkolas/skali/internal/revision"
	"github.com/Hinkolas/skali/internal/store"
)

const (
	schedulerBootDelay  = 30 * time.Second
	schedulerTick       = time.Minute
	schedulerTickBudget = 50 * time.Second
)

// revisionLoader is the slice of deploy.Service the controller reads.
type revisionLoader interface {
	GetRevision(ctx context.Context, id uuid.UUID) (*revision.Revision, error)
}

// Scheduler turns environment backup schedules into scheduled backup runs.
// Once a minute it walks every active environment and creates a backup for
// each one whose schedule fired since the scheduler last acted on it.
// Due-ness is persisted in backup_schedules, so a restart neither re-fires
// nor drifts; a schedule seen for the first time, or a changed expression,
// is seeded to fire at its next cron time rather than immediately.
type Scheduler struct {
	controller *Controller
	st         *store.Store
	now        func() time.Time
	logger     *slog.Logger

	warnedNoTarget bool
}

// NewScheduler builds the scheduler beside a controller; Run starts it.
func NewScheduler(c *Controller) *Scheduler {
	return &Scheduler{
		controller: c,
		st:         c.deps.Store,
	}
}

func (s *Scheduler) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Scheduler) log() *slog.Logger {
	if s.logger != nil {
		return s.logger
	}
	return slog.Default()
}

// Run ticks once a minute (cron granularity) after a short boot delay,
// until the context ends.
func (s *Scheduler) Run(ctx context.Context) {
	timer := time.NewTimer(schedulerBootDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	s.tick(ctx)
	ticker := time.NewTicker(schedulerTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

func (s *Scheduler) tick(ctx context.Context) {
	tickCtx, cancel := context.WithTimeout(ctx, schedulerTickBudget)
	defer cancel()
	if err := s.Tick(tickCtx); err != nil {
		s.log().WarnContext(ctx, "backup scheduler tick", "err", err)
	}
}

// Tick runs one pass at the scheduler's clock. Exported for tests.
func (s *Scheduler) Tick(ctx context.Context) error {
	now := s.clock()
	// Without a target there is nothing to write to: the local dev
	// platform runs this loop too and must stay quiet.
	if _, err := s.controller.deps.Targets.Get(ctx, DefaultTargetName); err != nil {
		if errors.Is(err, ErrTargetNotFound) {
			if !s.warnedNoTarget {
				s.log().DebugContext(ctx, "backup scheduler idle: no backup target is configured")
				s.warnedNoTarget = true
			}
			return nil
		}
		return fmt.Errorf("read backup target: %w", err)
	}
	s.warnedNoTarget = false

	environments, err := s.st.ListActiveEnvironmentRevisions(ctx)
	if err != nil {
		return fmt.Errorf("list active environments: %w", err)
	}
	rows, err := s.st.ListBackupSchedules(ctx)
	if err != nil {
		return fmt.Errorf("list backup schedules: %w", err)
	}
	// Rows still in state after the walk belong to environments that are
	// no longer active or whose schedule is off; they are dropped so a
	// schedule that comes back later is seeded afresh instead of catching
	// up on fires it missed while off.
	state := make(map[uuid.UUID]store.BackupSchedule, len(rows))
	for _, row := range rows {
		state[row.EnvironmentID] = row
	}

	for _, environment := range environments {
		if environment.BackupSchedule == "" {
			continue
		}
		row, seen := state[environment.EnvironmentID]
		delete(state, environment.EnvironmentID)
		schedule, err := cron.Parse(environment.BackupSchedule)
		if err != nil {
			// The setting was validated on write; a failure here means a
			// stored expression predates the parser. Skip, do not crash.
			s.log().WarnContext(ctx, "backup scheduler: unparseable schedule",
				"environment", environment.Name, "schedule", environment.BackupSchedule, "err", err)
			continue
		}
		if !seen || row.Schedule != environment.BackupSchedule {
			if err := s.st.UpsertBackupSchedule(ctx, store.UpsertBackupScheduleParams{
				EnvironmentID: environment.EnvironmentID, Schedule: environment.BackupSchedule, LastFireAt: now,
			}); err != nil {
				return fmt.Errorf("seed backup schedule: %w", err)
			}
			event := "backup schedule seeded"
			if seen {
				event = "backup schedule changed, reseeded"
			}
			s.log().InfoContext(ctx, event,
				"environment", environment.Name, "schedule", environment.BackupSchedule,
				"first_run", schedule.Next(now).Format(time.RFC3339))
			continue
		}
		fire, due := nextDue(schedule, row.LastFireAt, now)
		if !due {
			continue
		}
		result, err := s.controller.CreateBackup(ctx, BackupInput{
			EnvironmentID: environment.EnvironmentID,
			Actor:         ScheduleActor,
			Trigger:       TriggerScheduled,
		})
		if err != nil {
			// Nothing advances: the fire is retried on the next tick
			// until the environment is free or the reason clears.
			switch {
			case errors.Is(err, ErrBackupInFlight):
				s.log().DebugContext(ctx, "backup scheduler: environment busy, retrying next minute",
					"environment", environment.Name)
			case errors.Is(err, ErrEnvironmentNotActive), errors.Is(err, ErrEnvironmentNotFound),
				errors.Is(err, ErrScheduleNotSet), errors.Is(err, ErrNothingToBackUp):
				s.log().DebugContext(ctx, "backup scheduler: skipped",
					"environment", environment.Name, "reason", err)
			case errors.Is(err, ErrTargetNotFound):
				return nil
			default:
				s.log().WarnContext(ctx, "backup scheduler: create backup",
					"environment", environment.Name, "err", err)
			}
			continue
		}
		backupID := result.BackupID
		if err := s.st.UpsertBackupSchedule(ctx, store.UpsertBackupScheduleParams{
			EnvironmentID: environment.EnvironmentID, Schedule: environment.BackupSchedule,
			LastFireAt: fire, LastBackupID: &backupID,
		}); err != nil {
			return fmt.Errorf("advance backup schedule: %w", err)
		}
		s.log().InfoContext(ctx, "scheduled backup started",
			"environment", environment.Name, "run", result.RunID, "fire", fire.Format(time.RFC3339))
	}

	for environmentID := range state {
		if err := s.st.DeleteBackupSchedule(ctx, environmentID); err != nil {
			return fmt.Errorf("drop backup schedule: %w", err)
		}
	}
	return nil
}

// nextDue reports whether the schedule fired since lastFire and, if so, the
// most recent fire at or before now. Several missed fires collapse into the
// latest one, which is the single catch-up a daemon owes after downtime.
func nextDue(schedule *cron.Schedule, lastFire, now time.Time) (time.Time, bool) {
	fire := schedule.Next(lastFire)
	if fire.IsZero() || fire.After(now) {
		return time.Time{}, false
	}
	for {
		following := schedule.Next(fire)
		if following.IsZero() || following.After(now) {
			return fire, true
		}
		fire = following
	}
}
