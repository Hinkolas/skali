-- CreateAttempt opens the next attempt of a step while its run is open,
-- with the run row held KEY SHARE like every journal write that adds a row
-- under a run, so a finish waits for it (see LockRunForFinish). Under a
-- finished run nothing is written and no row returns.
-- name: CreateAttempt :one
WITH open_run AS (
    SELECT runs.id FROM runs
    JOIN steps ON steps.run_id = runs.id
    WHERE steps.id = sqlc.arg(step_id)::uuid AND runs.status IN ('pending', 'running')
    FOR KEY SHARE OF runs
)
INSERT INTO attempts (id, step_id, number, executor_id)
SELECT sqlc.arg(id)::uuid, sqlc.arg(step_id)::uuid, next.number, sqlc.arg(executor_id)::text
FROM (SELECT COALESCE(MAX(attempts.number), 0) + 1 AS number
      FROM attempts WHERE attempts.step_id = sqlc.arg(step_id)::uuid) AS next
WHERE EXISTS (SELECT 1 FROM open_run)
RETURNING *;

-- name: GetAttemptByID :one
SELECT * FROM attempts WHERE id = $1;

-- name: GetAttemptForUpdate :one
SELECT * FROM attempts WHERE id = $1 FOR UPDATE;

-- name: MarkAttemptFinished :exec
UPDATE attempts SET status = $2, finished_at = now() WHERE id = $1;

-- FinishAttempt is the guarded close: it applies only from one of
-- from_statuses, in one statement, and returns the run to notify; no row
-- returns otherwise.
-- name: FinishAttempt :one
UPDATE attempts SET status = sqlc.arg(status)::text, finished_at = now()
FROM steps
WHERE attempts.id = sqlc.arg(id) AND steps.id = attempts.step_id
  AND attempts.status = ANY(sqlc.arg(from_statuses)::text[])
RETURNING steps.run_id;

-- name: ListAttemptsByRun :many
SELECT attempts.* FROM attempts
JOIN steps ON steps.id = attempts.step_id
WHERE steps.run_id = $1
ORDER BY attempts.started_at;

-- Recovery: every running attempt owned by a dead executor.
-- name: ListForeignRunningAttempts :many
SELECT * FROM attempts WHERE status = 'running' AND executor_id <> $1;

-- The client log surface appends into a step's running attempt (the
-- partial unique index admits at most one).
-- name: GetRunningAttemptByStep :one
SELECT * FROM attempts WHERE step_id = $1 AND status = 'running';
