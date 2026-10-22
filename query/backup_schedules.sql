-- Every schedule with the outcome of the backup it last started, which
-- decides whether a failed fire is retried.
-- name: ListBackupSchedules :many
SELECT s.environment_id, s.schedule, s.last_fire_at, s.last_backup_id, s.attempts, s.updated_at,
       b.status AS last_backup_status, b.finished_at AS last_backup_finished_at
FROM backup_schedules s
LEFT JOIN backups b ON b.id = s.last_backup_id;

-- Seed or advance one environment's state; the scheduler is the only writer.
-- name: UpsertBackupSchedule :exec
INSERT INTO backup_schedules (environment_id, schedule, last_fire_at, last_backup_id, attempts)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (environment_id) DO UPDATE
SET schedule = EXCLUDED.schedule,
    last_fire_at = EXCLUDED.last_fire_at,
    last_backup_id = EXCLUDED.last_backup_id,
    attempts = EXCLUDED.attempts,
    updated_at = now();

-- Record a retry of the current fire: the fire stays, the backup and the
-- count move on.
-- name: RetryBackupSchedule :exec
UPDATE backup_schedules
SET last_backup_id = $2,
    attempts = attempts + 1,
    updated_at = now()
WHERE environment_id = $1;

-- name: DeleteBackupSchedule :exec
DELETE FROM backup_schedules WHERE environment_id = $1;
