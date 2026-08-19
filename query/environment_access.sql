-- name: UpsertEnvironmentAccess :one
INSERT INTO environment_access (environment_id, project_id, user_id, role)
VALUES ($1, $2, $3, $4)
ON CONFLICT (environment_id, user_id) DO UPDATE
    SET role = EXCLUDED.role, updated_at = now()
RETURNING *;

-- One environment's cells with the user columns the access surfaces show.
-- name: ListEnvironmentAccess :many
SELECT a.environment_id, a.project_id, a.user_id, a.role, a.created_at, a.updated_at, u.email, u.name
FROM environment_access a
JOIN users u ON u.id = a.user_id
WHERE a.environment_id = $1
ORDER BY lower(u.email);

-- Every cell of one user, the resolver's second read.
-- name: ListEnvironmentAccessForUser :many
SELECT * FROM environment_access WHERE user_id = $1;

-- name: DeleteEnvironmentAccess :execrows
DELETE FROM environment_access WHERE environment_id = $1 AND user_id = $2;
