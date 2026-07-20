-- name: CreateEnvironment :one
INSERT INTO environments (id, project_id, name)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetEnvironmentByID :one
SELECT * FROM environments WHERE id = $1;

-- name: ListEnvironments :many
SELECT * FROM environments WHERE project_id = $1 ORDER BY name;

-- name: DeleteEnvironmentByID :execrows
DELETE FROM environments WHERE id = $1;
