package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/cron"
	"github.com/Hinkolas/skali/internal/revision"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/utils"
)

const (
	schedulerBootDelay  = 30 * time.Second
	schedulerTick       = time.Minute
	schedulerTickBudget = 50 * time.Second
)

// revisionLoader is the slice of deploy.Service the scheduler reads.
type revisionLoader interface {
	GetRevision(ctx context.Context, id uuid.UUID) (*revision.Revision, error)
}

// Scheduler turns manifest backup policies into scheduled backup runs. Once
// a minute it walks every active environment, parses the policies of its
// active revision (cached per revision), and creates a backup for every
// policy whose cron fired since the scheduler last acted on it. Due-ness is
// persisted in backup_schedules, so a restart neither re-fires nor drifts;
// a policy seen for the first time is seeded to fire at its next cron time.
type Scheduler struct {
	controller *Controller
	st         *store.Store
	revisions  revisionLoader
	now        func() time.Time
	logger     *slog.Logger

	policies       map[uuid.UUID]*cachedPolicies
	warnedNoTarget bool
}

type cachedPolicies struct {
	revisionID uuid.UUID
	byKey      map[string]compiledPolicy
}

type compiledPolicy struct {
	backup   compiler.Backup
	schedule *cron.Schedule
}

// NewScheduler builds the scheduler beside a controller; Run starts it.
func NewScheduler(c *Controller) *Scheduler {
	return &Scheduler{
		controller: c,
		st:         c.deps.Store,
		revisions:  c.revisions,
		policies:   make(map[uuid.UUID]*cachedPolicies),
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
	type scheduleKey struct {
		environment uuid.UUID
		policy      string
	}
	state := make(map[scheduleKey]store.BackupSchedule, len(rows))
	for _, row := range rows {
		state[scheduleKey{row.EnvironmentID, row.Policy}] = row
	}

	active := make(map[uuid.UUID]bool, len(environments))
	for _, environment := range environments {
		active[environment.EnvironmentID] = true
		policies, err := s.policiesFor(ctx, environment.EnvironmentID, *environment.ActiveRevisionID)
		if err != nil {
			s.log().WarnContext(ctx, "backup scheduler: read policies", "environment", environment.Name, "err", err)
			continue
		}
		for _, key := range utils.SortedKeys(policies) {
			policy := policies[key]
			row, seen := state[scheduleKey{environment.EnvironmentID, key}]
			delete(state, scheduleKey{environment.EnvironmentID, key})
			if !seen {
				if err := s.st.UpsertBackupSchedule(ctx, store.UpsertBackupScheduleParams{
					EnvironmentID: environment.EnvironmentID, Policy: key, LastFireAt: now,
				}); err != nil {
					return fmt.Errorf("seed backup schedule: %w", err)
				}
				s.log().InfoContext(ctx, "backup policy scheduled",
					"environment", environment.Name, "policy", key,
					"first_run", policy.schedule.Next(now).Format(time.RFC3339))
				continue
			}
			fire, due := nextDue(policy.schedule, row.LastFireAt, now)
			if !due {
				continue
			}
			result, err := s.controller.CreateBackup(ctx, BackupInput{
				EnvironmentID: environment.EnvironmentID,
				Actor:         ScheduleActor(key),
				Trigger:       TriggerScheduled,
				Policy:        key,
			})
			if err != nil {
				// Nothing advances: the fire is retried on the next tick
				// until the environment is free or the reason clears.
				switch {
				case errors.Is(err, ErrBackupInFlight):
					s.log().DebugContext(ctx, "backup scheduler: environment busy, retrying next minute",
						"environment", environment.Name, "policy", key)
				case errors.Is(err, ErrEnvironmentNotActive), errors.Is(err, ErrEnvironmentNotFound),
					errors.Is(err, ErrPolicyNotFound), errors.Is(err, ErrNothingToBackUp):
					s.log().DebugContext(ctx, "backup scheduler: skipped",
						"environment", environment.Name, "policy", key, "reason", err)
				case errors.Is(err, ErrTargetNotFound):
					return nil
				default:
					s.log().WarnContext(ctx, "backup scheduler: create backup",
						"environment", environment.Name, "policy", key, "err", err)
				}
				continue
			}
			backupID := result.BackupID
			if err := s.st.UpsertBackupSchedule(ctx, store.UpsertBackupScheduleParams{
				EnvironmentID: environment.EnvironmentID, Policy: key, LastFireAt: fire, LastBackupID: &backupID,
			}); err != nil {
				return fmt.Errorf("advance backup schedule: %w", err)
			}
			s.log().InfoContext(ctx, "scheduled backup started",
				"environment", environment.Name, "policy", key, "run", result.RunID, "fire", fire.Format(time.RFC3339))
		}
	}

	// Rows left over belong to policies the active revision no longer
	// declares, or to environments no longer active; drop them so a policy
	// that comes back later is seeded afresh instead of catching up.
	for key := range state {
		if err := s.st.DeleteBackupSchedule(ctx, store.DeleteBackupScheduleParams{
			EnvironmentID: key.environment, Policy: key.policy,
		}); err != nil {
			return fmt.Errorf("drop backup schedule: %w", err)
		}
	}
	for environmentID := range s.policies {
		if !active[environmentID] {
			delete(s.policies, environmentID)
		}
	}
	return nil
}

// policiesFor returns the environment's parsed policies, decoding the
// revision only when the active pointer moved since the last tick.
func (s *Scheduler) policiesFor(ctx context.Context, environmentID, revisionID uuid.UUID) (map[string]compiledPolicy, error) {
	if cached, ok := s.policies[environmentID]; ok && cached.revisionID == revisionID {
		return cached.byKey, nil
	}
	revisionDoc, err := s.revisions.GetRevision(ctx, revisionID)
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]compiledPolicy, len(revisionDoc.Definition.Backups))
	for key, policy := range revisionDoc.Definition.Backups {
		schedule, err := cron.Parse(policy.Schedule)
		if err != nil {
			// The compiler validated this expression; a failure here means
			// a stored revision predates the parser. Skip, do not crash.
			s.log().WarnContext(ctx, "backup scheduler: unparseable schedule", "policy", key, "schedule", policy.Schedule, "err", err)
			continue
		}
		byKey[key] = compiledPolicy{backup: policy, schedule: schedule}
	}
	s.policies[environmentID] = &cachedPolicies{revisionID: revisionID, byKey: byKey}
	return byKey, nil
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
