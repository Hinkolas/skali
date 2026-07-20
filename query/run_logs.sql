-- Log entries are append-only; seq is allocated by the journal writer under
-- the attempt row lock, so it is gap-free and monotonic per attempt.

-- name: AppendRunLog :one
INSERT INTO run_logs (id, attempt_id, seq, level, message, fields)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetMaxRunLogSeq :one
SELECT COALESCE(MAX(seq), 0)::bigint FROM run_logs WHERE attempt_id = $1;

-- Step logs across attempts, ordered by (attempt number, seq). The cursor
-- is the same composite, so pagination and SSE resume share one shape.
-- name: ListStepLogs :many
SELECT run_logs.id, run_logs.attempt_id, attempts.number AS attempt_number,
       run_logs.seq, run_logs.ts, run_logs.level, run_logs.message, run_logs.fields
FROM run_logs
JOIN attempts ON attempts.id = run_logs.attempt_id
WHERE attempts.step_id = $1
  AND (attempts.number, run_logs.seq) > (sqlc.arg(after_number)::bigint, sqlc.arg(after_seq)::bigint)
ORDER BY attempts.number, run_logs.seq
LIMIT sqlc.arg(row_limit);
