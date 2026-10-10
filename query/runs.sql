-- name: CreateRun :one
INSERT INTO runs (id, kind, project_id, environment_id, actor, bypass_protection)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- BeginRun creates a run already running. The partial unique index turns
-- a second running run for the same environment into a unique violation,
-- and then no row exists.
-- name: BeginRun :one
INSERT INTO runs (id, kind, project_id, environment_id, actor, bypass_protection, status, started_at)
VALUES ($1, $2, $3, $4, $5, $6, 'running', now())
RETURNING *;

-- name: GetRunByID :one
SELECT * FROM runs WHERE id = $1;

-- Row lock so status transitions are guarded under the lifecycle machine.
-- name: GetRunForUpdate :one
SELECT * FROM runs WHERE id = $1 FOR UPDATE;

-- name: MarkRunRunning :exec
UPDATE runs SET status = 'running', started_at = now() WHERE id = $1;

-- LockRunForFinish is the first of a finish's two statements (see
-- store.FinishRun). FOR UPDATE conflicts with the KEY SHARE lock every
-- journal write that adds a step or an attempt holds on its run, so the
-- finish waits until those writes commit, and later ones wait for the
-- finish.
-- name: LockRunForFinish :exec
SELECT 1 FROM runs WHERE id = $1 FOR UPDATE;

-- FinishRun moves a run to a terminal status, and every non-terminal step
-- and attempt of the run with it: running work adopts close_status,
-- unstarted steps are skipped. It applies only from one of from_statuses,
-- which the journal derives from its run machine; otherwise nothing is
-- written and no row returns. The failure text is only meaningful with
-- status 'failed'; the journal passes NULL for every other terminal
-- status. Send it through store.FinishRun, after LockRunForFinish: on its
-- own its snapshot misses a step whose insert has not committed yet.
-- name: FinishRun :one
WITH run AS (
    UPDATE runs SET status = sqlc.arg(status)::text, finished_at = now(), failure = sqlc.narg(failure)::text
    WHERE runs.id = sqlc.arg(id)::uuid AND runs.status = ANY(sqlc.arg(from_statuses)::text[])
    RETURNING runs.id, runs.environment_id
), closed_attempts AS (
    UPDATE attempts SET status = sqlc.arg(close_status)::text, finished_at = now()
    FROM steps, run
    WHERE attempts.step_id = steps.id AND steps.run_id = run.id AND attempts.status = 'running'
), closed_steps AS (
    UPDATE steps SET
        status = CASE WHEN steps.status = 'running' THEN sqlc.arg(close_status)::text ELSE 'skipped' END,
        finished_at = now()
    FROM run
    WHERE steps.run_id = run.id AND steps.status IN ('pending', 'waiting', 'running')
)
SELECT run.environment_id FROM run;

-- A run that never started explains nothing and nothing will ever finish
-- it: the creator removes the row instead of stranding it pending, which
-- the terminal-only retention below would never reclaim. Guarded on the
-- status so a run that did start is never deleted underneath its writer.
-- name: ListRunsByEnvironment :many
SELECT * FROM runs WHERE environment_id = $1 ORDER BY created_at DESC;

-- Journal attachment for the reconcile worker: adopt the environment's
-- running deployment run when one exists. Explanatory only; reconciliation
-- decisions never read this.
-- name: GetRunningRunByEnvironment :one
SELECT * FROM runs WHERE environment_id = $1 AND status = 'running';

-- Retention: drop terminal runs beyond the newest keep-count of one
-- environment (none when environment_id is NULL), and terminal runs older
-- than the age cutoff anywhere.
-- name: PruneRuns :exec
WITH excess AS (
    DELETE FROM runs WHERE runs.id IN (
        SELECT terminal.id FROM runs AS terminal
        WHERE terminal.environment_id = sqlc.narg(environment_id)::uuid
          AND terminal.status IN ('succeeded', 'failed', 'cancelled')
        ORDER BY terminal.created_at DESC
        OFFSET sqlc.arg(keep)::bigint
    )
    RETURNING runs.id
)
DELETE FROM runs
WHERE runs.status IN ('succeeded', 'failed', 'cancelled') AND runs.finished_at < sqlc.arg(cutoff)::timestamptz
  AND runs.id NOT IN (SELECT excess.id FROM excess);

-- Platform updates wait for in-flight work: a running run anywhere means a
-- deployment, restore, or restart the control-plane roll would interrupt.
-- name: CountRunningRuns :one
SELECT count(*) FROM runs WHERE status = 'running';

-- Boot recovery for controllers that own runs of one kind without a
-- durable row of their own (credential rotations): a running run whose
-- worker died with the previous process would hold the environment's
-- one-running-run slot forever.
-- name: ListRunningRunsByKind :many
SELECT * FROM runs WHERE kind = $1 AND status = 'running' ORDER BY created_at;
