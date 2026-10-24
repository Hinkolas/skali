-- name: CreateBackup :one
INSERT INTO backups (id, kind, environment_id, project_name, environment_name,
                     revision_id, run_id, trigger, strategy, retention_seconds, environment_namespace)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
        (SELECT backup_namespace FROM environments WHERE id = $3))
RETURNING *;

-- name: GetBackup :one
SELECT * FROM backups WHERE id = $1;

-- The from-status guard makes concurrent transitions explicit: a lost race
-- is 0 rows, never a silent overwrite.
-- name: SetBackupStatus :execrows
UPDATE backups
SET status = sqlc.arg(to_status),
    error = sqlc.arg(error),
    finished_at = CASE WHEN sqlc.arg(to_status) IN ('succeeded', 'failed', 'cancelled')
                       THEN now() END
WHERE id = $1 AND status = sqlc.arg(from_status);

-- name: GetBackupByRun :one
SELECT * FROM backups WHERE run_id = $1;

-- name: SetBackupSnapshotKey :exec
UPDATE backups SET snapshot_key = $2 WHERE id = $1;

-- name: SetBackupRevision :exec
UPDATE backups SET revision_id = $2 WHERE id = $1;

-- name: DeleteBackup :exec
DELETE FROM backups WHERE id = $1;

-- Recovery on boot: rows a dead daemon left behind.
-- name: ListUnfinishedBackups :many
SELECT * FROM backups WHERE status IN ('pending', 'running');

-- name: ListBackupsByEnvironment :many
SELECT * FROM backups
WHERE environment_id = $1
ORDER BY created_at DESC;

-- A backup or restore run holds its environment's one run slot until the
-- worker driving its row finishes it, and the worker finishes the run
-- before the row. A running run whose row is already terminal, or that has
-- no row at all once the accept path had time to record one, is stranded.
-- Accepting an incomplete restore's data runs without a row and finishes
-- within its request, so one still running after the grace is stranded
-- too.
-- name: ListStrandedBackupRuns :many
SELECT runs.id, runs.kind FROM runs
LEFT JOIN backups ON backups.run_id = runs.id
WHERE runs.kind IN ('backup', 'restore', 'restore-accept') AND runs.status = 'running'
  AND runs.created_at < sqlc.arg(cutoff)::timestamptz
  AND (backups.id IS NULL OR backups.status IN ('succeeded', 'failed', 'cancelled'))
ORDER BY runs.created_at;

-- name: CountBackupRuns :one
-- The Backups overview's activity: backups that finished since a point in
-- time, by outcome, and the backups and restores queued or under way.
SELECT count(*) FILTER (WHERE kind = 'backup' AND status = 'succeeded'
                          AND finished_at >= sqlc.arg(since)::timestamptz)::bigint AS succeeded,
       count(*) FILTER (WHERE kind = 'backup' AND status = 'failed'
                          AND finished_at >= sqlc.arg(since)::timestamptz)::bigint AS failed,
       count(*) FILTER (WHERE kind = 'backup' AND status IN ('pending', 'running'))::bigint AS backing_up,
       count(*) FILTER (WHERE kind = 'restore' AND status IN ('pending', 'running'))::bigint AS restoring
FROM backups;

-- name: LatestFinishedBackup :one
-- The newest backup run that ended with the given status.
SELECT * FROM backups
WHERE kind = 'backup' AND status = $1 AND finished_at IS NOT NULL
ORDER BY finished_at DESC
LIMIT 1;
