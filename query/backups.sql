-- name: CreateBackup :one
INSERT INTO backups (id, kind, environment_id, project_name, environment_name,
                     revision_id, run_id)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetBackup :one
SELECT * FROM backups WHERE id = $1;

-- The from-status guard makes concurrent transitions explicit: a lost race
-- is 0 rows, never a silent overwrite.
-- name: SetBackupStatus :execrows
UPDATE backups
SET status = sqlc.arg(to_status),
    error = sqlc.arg(error),
    finished_at = CASE WHEN sqlc.arg(to_status) IN ('succeeded', 'failed')
                       THEN now() END
WHERE id = $1 AND status = sqlc.arg(from_status);

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
