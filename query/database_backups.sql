-- name: CreateDatabaseBackup :one
INSERT INTO database_backups (id, claim_id, cluster_id, kind, run_id)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetDatabaseBackup :one
SELECT * FROM database_backups WHERE id = $1;

-- name: SetDatabaseBackupStatus :execrows
UPDATE database_backups
SET status = sqlc.arg(to_status),
    finished_at = CASE WHEN sqlc.arg(to_status) IN ('succeeded', 'failed')
                       THEN now() END
WHERE id = $1 AND status = sqlc.arg(from_status);

-- name: ListDatabaseBackupsByClaim :many
SELECT * FROM database_backups
WHERE claim_id = $1
ORDER BY created_at DESC;
