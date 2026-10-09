-- Steps are addressed by their deterministic (run_id, key); creation is
-- idempotent so a restarted controller reattaches instead of duplicating.

-- EnsureStep creates the step unless its (run_id, key) exists and returns
-- the step either way, in one round trip. The select cannot see this
-- statement's own insert, so the two halves never both return; a step
-- another transaction created concurrently can be missing from its
-- snapshot too, and then no row returns and the caller reads it.
-- name: EnsureStep :one
WITH inserted AS (
    INSERT INTO steps (id, run_id, parent_id, key, title)
    VALUES ($1, $2, $3, $4, $5)
    ON CONFLICT (run_id, key) DO NOTHING
    RETURNING *
)
SELECT * FROM inserted
UNION ALL
SELECT * FROM steps WHERE run_id = $2 AND key = $4;

-- name: GetStepByRunAndKey :one
SELECT * FROM steps WHERE run_id = $1 AND key = $2;

-- name: GetStepByID :one
SELECT * FROM steps WHERE id = $1;

-- started_at is stamped on the first entry into running; finished_at on
-- reaching a terminal status. The update applies only from one of
-- from_statuses, which the journal derives from its step machine, so the
-- guard and the change are one statement; otherwise no row returns.
-- name: SetStepStatus :one
UPDATE steps SET
    status = sqlc.arg(status)::text,
    started_at = CASE WHEN sqlc.arg(status)::text = 'running' THEN COALESCE(started_at, now()) ELSE started_at END,
    finished_at = CASE WHEN sqlc.arg(status)::text IN ('succeeded', 'failed', 'skipped', 'cancelled') THEN now() ELSE finished_at END
WHERE id = sqlc.arg(id) AND status = ANY(sqlc.arg(from_statuses)::text[])
RETURNING run_id;

-- CompleteStep journals work that already happened in one statement: the
-- step moves as if through running to status, with one finished attempt
-- carrying the log entries. It writes only while the step's status is one
-- of from_statuses and no attempt of the step is running; otherwise
-- nothing is written and no row returns.
-- name: CompleteStep :one
WITH step AS (
    UPDATE steps SET
        status = sqlc.arg(status)::text,
        started_at = COALESCE(started_at, now()),
        finished_at = now()
    WHERE steps.id = sqlc.arg(id)
      AND steps.status = ANY(sqlc.arg(from_statuses)::text[])
      AND NOT EXISTS (
          SELECT 1 FROM attempts WHERE attempts.step_id = steps.id AND attempts.status = 'running')
    RETURNING steps.id
), attempt AS (
    INSERT INTO attempts (id, step_id, number, status, executor_id, finished_at)
    SELECT sqlc.arg(attempt_id)::uuid, step.id,
           (SELECT COALESCE(MAX(number), 0) + 1 FROM attempts WHERE attempts.step_id = step.id),
           sqlc.arg(attempt_status)::text, sqlc.arg(executor_id)::text, now()
    FROM step
    RETURNING attempts.id, attempts.number
), entries AS (
    -- entries is a JSON array of {id, level, message, fields}; its order
    -- is the sequence.
    INSERT INTO run_logs (id, attempt_id, seq, level, message, fields)
    SELECT (entry.value->>'id')::uuid, attempt.id, entry.seq,
           entry.value->>'level', entry.value->>'message', entry.value->'fields'
    FROM attempt, jsonb_array_elements(sqlc.arg(entries)::jsonb) WITH ORDINALITY AS entry(value, seq)
)
SELECT attempt.number, now()::timestamptz AS logged_at FROM attempt;

-- name: SetStepProgress :exec
UPDATE steps SET progress_current = $2, progress_total = $3 WHERE id = $1;

-- name: ListStepsByRun :many
SELECT * FROM steps WHERE run_id = $1 ORDER BY created_at;

-- Terminality sweep on run finish: running steps adopt the run's outcome,
-- unstarted steps are skipped.
-- name: CloseRunningSteps :execrows
UPDATE steps SET status = $2, finished_at = now()
WHERE run_id = $1 AND status = 'running';

-- name: SkipUnstartedSteps :execrows
UPDATE steps SET status = 'skipped', finished_at = now()
WHERE run_id = $1 AND status IN ('pending', 'waiting');

-- CountDeferredRoutesByRun counts, per run, the TLS checkpoints that ended
-- skipped because the route's domain did not reach this installation
-- (their journal carries phase=deferred). A checkpoint skipped by the
-- run's own conclusion (a cancelled rollout) does not count.
-- name: CountDeferredRoutesByRun :many
SELECT steps.run_id, count(*)::bigint AS deferred
FROM steps
WHERE steps.run_id = ANY(sqlc.arg(run_ids)::uuid[])
  AND steps.key LIKE 'tls:%'
  AND steps.status = 'skipped'
  AND EXISTS (
      SELECT 1 FROM attempts
      JOIN run_logs ON run_logs.attempt_id = attempts.id
      WHERE attempts.step_id = steps.id AND run_logs.fields->>'phase' = 'deferred')
GROUP BY steps.run_id;
