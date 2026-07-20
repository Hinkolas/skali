-- name: CreateBuild :one
INSERT INTO builds (
    id, project_id, deployment_id, application, origin, status,
    platform, context_hash, config_hash, artifact_id, run_id, step_key,
    started_at, heartbeat_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
RETURNING *;

-- name: GetBuildByID :one
SELECT * FROM builds WHERE id = $1;

-- Row lock so status transitions are guarded under the lifecycle machine.
-- name: GetBuildForUpdate :one
SELECT * FROM builds WHERE id = $1 FOR UPDATE;

-- started_at is stamped on the first entry into running; finished_at on
-- reaching a terminal status.
-- name: SetBuildStatus :exec
UPDATE builds SET
    status = $2,
    started_at = CASE WHEN $2::text = 'running' THEN COALESCE(started_at, now()) ELSE started_at END,
    finished_at = CASE WHEN $2::text IN ('succeeded', 'failed', 'cancelled') THEN now() ELSE finished_at END,
    updated_at = now()
WHERE id = $1;

-- name: HeartbeatBuild :execrows
UPDATE builds SET heartbeat_at = now(), updated_at = now()
WHERE id = $1 AND status = 'running';

-- name: ListBuildsForDeployment :many
SELECT * FROM builds WHERE deployment_id = $1 ORDER BY created_at;

-- Artifact verification closes the build that produced the content.
-- name: GetBuildByArtifactID :one
SELECT * FROM builds WHERE artifact_id = $1 ORDER BY created_at DESC LIMIT 1;

-- Local running builds whose executor has gone quiet; the deployment
-- sweeper fails these together with their deployments.
-- name: ListStaleLocalBuilds :many
SELECT * FROM builds
WHERE origin = 'local' AND status = 'running' AND heartbeat_at < $1;
