-- name: CreateEnvironment :one
INSERT INTO environments (id, project_id, name, max_role, priority)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetEnvironmentByID :one
SELECT * FROM environments WHERE id = $1;

-- name: ListEnvironments :many
SELECT * FROM environments WHERE project_id = $1 ORDER BY name;

-- Environments of several projects at once, for the per-user access grant.
-- name: ListEnvironmentsForProjects :many
SELECT * FROM environments WHERE project_id = ANY(@project_ids::uuid[]) ORDER BY project_id, name;

-- name: UpdateEnvironmentSettings :one
UPDATE environments
SET max_role = $2, deploy_policy = $3, promote_from = $4, priority = $5,
    backup_schedule = $6, backup_retention_seconds = $7, backup_strategy = $8,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- Every environment joined with its target pointer state and revision
-- pointers, for the project list summary (one query across all projects,
-- not one per project). The checksums ride along so list consumers never
-- load a revision to name it.
-- name: ListEnvironmentsWithTargets :many
SELECT e.id, e.project_id, e.name, t.state,
       t.target_revision_id, tr.checksum AS target_checksum,
       t.active_revision_id, ar.checksum AS active_checksum
FROM environments e
JOIN environment_targets t ON t.environment_id = e.id
LEFT JOIN revisions tr ON tr.id = t.target_revision_id
LEFT JOIN revisions ar ON ar.id = t.active_revision_id
ORDER BY e.project_id, e.name;

-- One project's environments with their target pointer state, for the
-- environments listing's summary (one query per project, never one per
-- environment).
-- name: ListProjectEnvironmentStates :many
SELECT e.id, t.state
FROM environments e
JOIN environment_targets t ON t.environment_id = e.id
WHERE e.project_id = $1
ORDER BY e.name;

-- name: DeleteEnvironmentByID :execrows
DELETE FROM environments WHERE id = $1;

-- Environments the backup scheduler considers: active with a converged
-- revision to snapshot, with their backup setting.
-- name: ListActiveEnvironmentRevisions :many
SELECT e.id AS environment_id, e.project_id, e.name, t.active_revision_id,
       e.backup_schedule, e.backup_retention_seconds, e.backup_strategy
FROM environments e
JOIN environment_targets t ON t.environment_id = e.id
WHERE t.state = 'active' AND t.active_revision_id IS NOT NULL
ORDER BY e.project_id, e.name;
