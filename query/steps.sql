-- Steps are addressed by their deterministic (run_id, key); creation is
-- idempotent so a restarted controller reattaches instead of duplicating.

-- name: InsertStep :execrows
INSERT INTO steps (id, run_id, parent_id, key, title)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (run_id, key) DO NOTHING;

-- name: GetStepByRunAndKey :one
SELECT * FROM steps WHERE run_id = $1 AND key = $2;

-- name: GetStepByID :one
SELECT * FROM steps WHERE id = $1;

-- name: GetStepForUpdate :one
SELECT * FROM steps WHERE id = $1 FOR UPDATE;

-- started_at is stamped on the first entry into running; finished_at on
-- reaching a terminal status.
-- name: SetStepStatus :exec
UPDATE steps SET
    status = $2,
    started_at = CASE WHEN $2::text = 'running' THEN COALESCE(started_at, now()) ELSE started_at END,
    finished_at = CASE WHEN $2::text IN ('succeeded', 'failed', 'skipped', 'cancelled') THEN now() ELSE finished_at END
WHERE id = $1;

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
