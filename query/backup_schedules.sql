-- name: ListBackupSchedules :many
SELECT * FROM backup_schedules;

-- Seed or advance one policy's state; the scheduler is the only writer.
-- name: UpsertBackupSchedule :exec
INSERT INTO backup_schedules (environment_id, policy, last_fire_at, last_backup_id)
VALUES ($1, $2, $3, $4)
ON CONFLICT (environment_id, policy) DO UPDATE
SET last_fire_at = EXCLUDED.last_fire_at,
    last_backup_id = EXCLUDED.last_backup_id,
    updated_at = now();

-- name: DeleteBackupSchedule :exec
DELETE FROM backup_schedules WHERE environment_id = $1 AND policy = $2;
