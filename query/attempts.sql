-- name: CreateAttempt :one
INSERT INTO attempts (id, step_id, number, executor_id)
SELECT $1, $2, COALESCE(MAX(number), 0) + 1, $3
FROM attempts WHERE step_id = $2
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

-- Terminality sweep on run finish.
-- name: CloseRunningAttemptsForRun :execrows
UPDATE attempts SET status = $2, finished_at = now()
WHERE status = 'running' AND step_id IN (SELECT id FROM steps WHERE run_id = $1);

-- Recovery: every running attempt owned by a dead executor.
-- name: ListForeignRunningAttempts :many
SELECT * FROM attempts WHERE status = 'running' AND executor_id <> $1;

-- The client log surface appends into a step's running attempt (the
-- partial unique index admits at most one).
-- name: GetRunningAttemptByStep :one
SELECT * FROM attempts WHERE step_id = $1 AND status = 'running';
