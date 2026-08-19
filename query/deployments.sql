-- name: CreateDeployment :one
INSERT INTO deployments (
    id, project_id, environment_id, definition_version_id,
    candidate_id, run_id, actor, build_executor, actions, restart,
    local_applications, prune_values, bypass_protection
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING *;

-- name: GetDeploymentByID :one
SELECT * FROM deployments WHERE id = $1;

-- Row lock so status transitions are guarded under the lifecycle machine.
-- name: GetDeploymentForUpdate :one
SELECT * FROM deployments WHERE id = $1 FOR UPDATE;

-- name: GetPreparingDeploymentForEnvironment :one
SELECT * FROM deployments WHERE environment_id = $1 AND status = 'preparing';

-- name: SetDeploymentStatus :exec
UPDATE deployments SET status = $2, updated_at = now() WHERE id = $1;

-- name: SetDeploymentActions :exec
UPDATE deployments SET actions = $2, updated_at = now() WHERE id = $1;

-- name: SetDeploymentRevision :exec
UPDATE deployments SET revision_id = $2, updated_at = now() WHERE id = $1;

-- name: ListDeploymentsForEnvironment :many
SELECT * FROM deployments
WHERE environment_id = $1
ORDER BY created_at DESC
LIMIT $2;

-- name: GetDeploymentByRunID :one
SELECT * FROM deployments WHERE run_id = $1;

-- Liveness of the artifact window: build heartbeats and artifact
-- verifications touch the row so the sweeper only fails deployments whose
-- client has actually gone quiet.
-- name: TouchDeployment :execrows
UPDATE deployments SET updated_at = now() WHERE id = $1 AND status = 'preparing';

-- name: ListStalePreparingDeployments :many
SELECT * FROM deployments WHERE status = 'preparing' AND updated_at < $1;
