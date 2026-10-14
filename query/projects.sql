-- name: CreateProject :one
INSERT INTO projects (id, name, display_name, source_mode)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetProjectByID :one
SELECT * FROM projects WHERE id = $1;

-- name: GetProjectForUpdate :one
SELECT * FROM projects WHERE id = $1 FOR UPDATE;

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

-- Marking keeps the first decision's time, so a repeated delete is a no-op.
-- name: MarkProjectDeleting :one
UPDATE projects SET deleting_at = COALESCE(deleting_at, now())
WHERE id = $1
RETURNING *;

-- The end of a project delete: the row goes once it is marked and its last
-- environment is gone. Callers hold the project row lock, so two purges
-- concluding together cannot both see the other's environment remaining.
-- name: DeleteDeletingProjectIfEmpty :execrows
DELETE FROM projects p
WHERE p.id = $1 AND p.deleting_at IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM environments e WHERE e.project_id = p.id);
