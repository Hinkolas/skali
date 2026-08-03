-- name: CreateEnvironment :one
INSERT INTO environments (id, project_id, name)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetEnvironmentByID :one
SELECT * FROM environments WHERE id = $1;

-- name: ListEnvironments :many
SELECT * FROM environments WHERE project_id = $1 ORDER BY name;

-- Every environment joined with its target pointer state, for the project
-- list summary (one query across all projects, not one per project).
-- name: ListEnvironmentsWithTargets :many
SELECT e.id, e.project_id, e.name, t.state
FROM environments e
JOIN environment_targets t ON t.environment_id = e.id
ORDER BY e.project_id, e.name;

-- name: DeleteEnvironmentByID :execrows
DELETE FROM environments WHERE id = $1;
