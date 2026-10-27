-- name: PutBuildInsights :exec
-- A retried upload replaces the earlier report.
INSERT INTO build_insights (step_id, outcome, engine, platform, started_at, finished_at,
                            error, steps, log_gzip, log_lines, log_truncated)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (step_id) DO UPDATE SET
    outcome = EXCLUDED.outcome,
    engine = EXCLUDED.engine,
    platform = EXCLUDED.platform,
    started_at = EXCLUDED.started_at,
    finished_at = EXCLUDED.finished_at,
    error = EXCLUDED.error,
    steps = EXCLUDED.steps,
    log_gzip = EXCLUDED.log_gzip,
    log_lines = EXCLUDED.log_lines,
    log_truncated = EXCLUDED.log_truncated,
    created_at = now();

-- name: GetBuildInsights :one
SELECT * FROM build_insights WHERE step_id = $1;
