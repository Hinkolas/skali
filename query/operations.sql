-- name: InsertOperation :one
INSERT INTO operations (id, kind, subject, created_by, expires_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetOperationByID :one
SELECT * FROM operations WHERE id = $1;

-- Newest first, capped by the caller; both filters optional.
-- name: ListOperations :many
SELECT * FROM operations
WHERE (sqlc.narg('kind')::text IS NULL OR kind = sqlc.narg('kind'))
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
ORDER BY created_at DESC
LIMIT $1;

-- The status guard makes completion single-shot: a row already failed by
-- FailExpiredOperations stays failed even if a zombie goroutine reports in
-- later.
-- name: CompleteOperation :execrows
UPDATE operations
SET status = $2, result = $3, error = $4, updated_at = now(), finished_at = now()
WHERE id = $1 AND status = 'running';

-- Liveness backstop (hygiene sweep): work runs in-process, so a running row
-- past its deadline belongs to a dead or wedged master.
-- name: FailExpiredOperations :execrows
UPDATE operations
SET status = 'failed', error = 'timed out or interrupted by a master restart',
    updated_at = now(), finished_at = now()
WHERE status = 'running' AND expires_at < now();

-- name: PruneFinishedOperations :execrows
DELETE FROM operations
WHERE status <> 'running' AND finished_at < now() - INTERVAL '24 hours';
