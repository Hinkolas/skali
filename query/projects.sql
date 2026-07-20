-- name: CreateProject :one
INSERT INTO projects (id, name, display_name, source_mode)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetProjectByID :one
SELECT * FROM projects WHERE id = $1;

-- name: GetProjectByName :one
SELECT * FROM projects WHERE name = $1;

-- name: ListProjects :many
SELECT * FROM projects ORDER BY name;

-- name: UpdateProject :one
UPDATE projects SET display_name = $2, source_mode = $3, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteProjectByID :execrows
DELETE FROM projects WHERE id = $1;
