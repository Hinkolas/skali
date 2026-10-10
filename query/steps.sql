-- Steps are addressed by their deterministic (run_id, key); creation is
-- idempotent so a restarted controller reattaches instead of duplicating.

-- EnsureStep creates the step unless its (run_id, key) exists and returns
-- the step either way, in one round trip. The select cannot see this
-- statement's own insert, so the two halves never both return; a step
-- another transaction created concurrently can be missing from its
-- snapshot too, and then no row returns and the caller reads it. A step is
-- created only while its run is open, with the run row held KEY SHARE (see
-- LockRunForFinish); a finished run gets none.
-- name: EnsureStep :one
WITH open_run AS (
    SELECT runs.id FROM runs
    WHERE runs.id = sqlc.arg(run_id)::uuid AND runs.status IN ('pending', 'running')
    FOR KEY SHARE
), inserted AS (
    INSERT INTO steps (id, run_id, parent_id, key, title)
    SELECT sqlc.arg(id)::uuid, open_run.id, sqlc.narg(parent_id)::uuid, sqlc.arg(key)::text, sqlc.arg(title)::text
    FROM open_run
    ON CONFLICT (run_id, key) DO NOTHING
    RETURNING *
)
SELECT * FROM inserted
UNION ALL
SELECT * FROM steps WHERE steps.run_id = sqlc.arg(run_id)::uuid AND steps.key = sqlc.arg(key)::text;

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

-- RecordStep journals one observation of a step addressed by its key, in
-- one statement. It creates the step at status when the run has none under
-- the key; otherwise it moves the step to status, provided its status is
-- one of from_statuses and none of its attempts is running. Either way it
-- writes one finished attempt carrying the entries, unless status is
-- running: a step that starts has no finished work yet. With dedupe set, the
-- attempt is written only when the last entry differs from the step's
-- latest line, and a step already at status with an unchanged line is not
-- written at all. The row reports the status the step had (empty: none),
-- whether anything was written, and the attempt's number (0: none). A step
-- another transaction created after this statement's snapshot is neither
-- found nor created: previous_status is empty and nothing is written.
-- Nothing is written either once the run has finished (run_open is false):
-- the record holds the run row KEY SHARE while its run is open, so a
-- finish waits for it (see LockRunForFinish).
-- name: RecordStep :one
WITH open_run AS (
    SELECT runs.id FROM runs
    WHERE runs.id = sqlc.arg(run_id)::uuid AND runs.status IN ('pending', 'running')
    FOR KEY SHARE
), existing AS (
    SELECT steps.id, steps.status FROM steps
    WHERE steps.run_id = sqlc.arg(run_id)::uuid AND steps.key = sqlc.arg(key)::text
), fresh AS (
    -- entries is a JSON array of {id, level, message, fields}; its order
    -- is the sequence.
    SELECT NOT sqlc.arg(dedupe)::boolean OR (
        SELECT run_logs.message FROM run_logs
        JOIN attempts ON attempts.id = run_logs.attempt_id
        JOIN existing ON existing.id = attempts.step_id
        ORDER BY attempts.number DESC, run_logs.seq DESC
        LIMIT 1
    ) IS DISTINCT FROM ((sqlc.arg(entries)::jsonb -> -1) ->> 'message') AS lines
), inserted AS (
    INSERT INTO steps (id, run_id, parent_id, key, title, status, started_at, finished_at)
    SELECT sqlc.arg(id)::uuid, sqlc.arg(run_id)::uuid, sqlc.narg(parent_id)::uuid,
           sqlc.arg(key)::text, sqlc.arg(title)::text, sqlc.arg(status)::text,
           CASE WHEN sqlc.arg(status)::text IN ('running', 'succeeded', 'failed') THEN now() END,
           CASE WHEN sqlc.arg(status)::text IN ('succeeded', 'failed', 'skipped', 'cancelled') THEN now() END
    WHERE NOT EXISTS (SELECT 1 FROM existing) AND EXISTS (SELECT 1 FROM open_run)
    ON CONFLICT (run_id, key) DO NOTHING
    RETURNING steps.id
), updated AS (
    UPDATE steps SET
        status = sqlc.arg(status)::text,
        started_at = CASE WHEN sqlc.arg(status)::text IN ('running', 'succeeded', 'failed')
            THEN COALESCE(steps.started_at, now()) ELSE steps.started_at END,
        finished_at = CASE WHEN sqlc.arg(status)::text IN ('succeeded', 'failed', 'skipped', 'cancelled')
            THEN now() ELSE steps.finished_at END
    FROM existing, fresh
    WHERE steps.id = existing.id
      AND steps.status = ANY(sqlc.arg(from_statuses)::text[])
      AND (steps.status <> sqlc.arg(status)::text OR fresh.lines)
      AND EXISTS (SELECT 1 FROM open_run)
      AND NOT EXISTS (
          SELECT 1 FROM attempts WHERE attempts.step_id = steps.id AND attempts.status = 'running')
    RETURNING steps.id
), step AS (
    SELECT inserted.id FROM inserted
    UNION ALL
    SELECT updated.id FROM updated
), attempt AS (
    INSERT INTO attempts (id, step_id, number, status, executor_id, finished_at)
    SELECT sqlc.arg(attempt_id)::uuid, step.id,
           (SELECT COALESCE(MAX(number), 0) + 1 FROM attempts WHERE attempts.step_id = step.id),
           sqlc.arg(attempt_status)::text, sqlc.arg(executor_id)::text, now()
    FROM step, fresh
    WHERE fresh.lines AND sqlc.arg(status)::text <> 'running'
    RETURNING attempts.id, attempts.number
), entries AS (
    INSERT INTO run_logs (id, attempt_id, seq, level, message, fields)
    SELECT (entry.value->>'id')::uuid, attempt.id, entry.seq,
           entry.value->>'level', entry.value->>'message', entry.value->'fields'
    FROM attempt, jsonb_array_elements(sqlc.arg(entries)::jsonb) WITH ORDINALITY AS entry(value, seq)
)
SELECT COALESCE((SELECT existing.id FROM existing), sqlc.arg(id)::uuid)::uuid AS step_id,
       COALESCE((SELECT existing.status FROM existing), '')::text AS previous_status,
       EXISTS (SELECT 1 FROM step) AS written,
       EXISTS (SELECT 1 FROM open_run) AS run_open,
       EXISTS (
           SELECT 1 FROM attempts JOIN existing ON existing.id = attempts.step_id
           WHERE attempts.status = 'running') AS attempt_running,
       COALESCE((SELECT attempt.number FROM attempt), 0)::bigint AS attempt_number,
       now()::timestamptz AS logged_at;

-- ListStepStates reads a run's steps for the kernel's view of them: each
-- step's status and, while it has not started, its latest line, which the
-- next observation of a waiting step is compared with.
-- name: ListStepStates :many
SELECT steps.id, steps.key, steps.status,
       COALESCE(CASE WHEN steps.status IN ('pending', 'waiting') THEN (
           SELECT run_logs.message FROM run_logs
           JOIN attempts ON attempts.id = run_logs.attempt_id
           WHERE attempts.step_id = steps.id
           ORDER BY attempts.number DESC, run_logs.seq DESC
           LIMIT 1) END, '')::text AS latest
FROM steps
WHERE steps.run_id = $1;

-- name: SetStepProgress :exec
UPDATE steps SET progress_current = $2, progress_total = $3 WHERE id = $1;

-- name: ListStepsByRun :many
SELECT * FROM steps WHERE run_id = $1 ORDER BY created_at;

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
