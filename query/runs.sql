-- name: CreateRun :one
INSERT INTO runs (id, kind, project_id, environment_id, actor, bypass_protection)
VALUES ($1, $2, $3, $4, $5, $6)
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

-- A run that never started explains nothing and nothing will ever finish
-- it: the creator removes the row instead of stranding it pending, which
-- the terminal-only retention below would never reclaim. Guarded on the
-- status so a run that did start is never deleted underneath its writer.
-- name: DeletePendingRun :execrows
DELETE FROM runs WHERE id = $1 AND status = 'pending';

-- name: ListRunsByEnvironment :many
SELECT * FROM runs WHERE environment_id = $1 ORDER BY created_at DESC;

-- Journal attachment for the reconcile worker: adopt the environment's
-- running deployment run when one exists. Explanatory only; reconciliation
-- decisions never read this.
-- name: GetRunningRunByEnvironment :one
SELECT * FROM runs WHERE environment_id = $1 AND status = 'running';

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

-- Platform updates wait for in-flight work: a running run anywhere means a
-- deployment, restore, or restart the control-plane roll would interrupt.
-- name: CountRunningRuns :one
SELECT count(*) FROM runs WHERE status = 'running';
