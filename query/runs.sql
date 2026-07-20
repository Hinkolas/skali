-- name: CreateRun :one
INSERT INTO runs (id, kind, project_id, environment_id, actor)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetRunByID :one
SELECT * FROM runs WHERE id = $1;

-- Row lock so status transitions are guarded under the lifecycle machine.
-- name: GetRunForUpdate :one
SELECT * FROM runs WHERE id = $1 FOR UPDATE;

-- name: MarkRunRunning :exec
UPDATE runs SET status = 'running', started_at = now() WHERE id = $1;

-- name: MarkRunFinished :exec
UPDATE runs SET status = $2, finished_at = now() WHERE id = $1;

-- name: ListRunsByEnvironment :many
SELECT * FROM runs WHERE environment_id = $1 ORDER BY created_at DESC;

-- Retention: drop terminal runs beyond the newest keep-count of one
-- environment, and terminal runs older than the age cutoff anywhere.
-- name: DeleteExcessTerminalRuns :execrows
DELETE FROM runs WHERE id IN (
    SELECT terminal.id FROM runs AS terminal
    WHERE terminal.environment_id = $1
      AND terminal.status IN ('succeeded', 'failed', 'cancelled')
    ORDER BY terminal.created_at DESC
    OFFSET $2
);

-- name: DeleteAgedTerminalRuns :execrows
DELETE FROM runs
WHERE status IN ('succeeded', 'failed', 'cancelled') AND finished_at < $1;
