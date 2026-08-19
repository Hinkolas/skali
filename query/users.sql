-- name: CreateUser :one
INSERT INTO users (id, email, name, role)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE lower(email) = lower($1);

-- name: SetUserTwoFactorEnabled :exec
UPDATE users SET two_factor_enabled = $2, updated_at = now() WHERE id = $1;

-- name: ListUsers :many
SELECT * FROM users ORDER BY created_at;

-- name: SetUserRole :one
UPDATE users SET role = $2, updated_at = now() WHERE id = $1
RETURNING *;

-- name: SetUserName :one
UPDATE users SET name = $2, updated_at = now() WHERE id = $1
RETURNING *;

-- name: SetUserCreateProjects :one
UPDATE users SET create_projects = $2, updated_at = now() WHERE id = $1
RETURNING *;

-- Locks the admin rows so concurrent demote/delete transactions serialize on
-- the last-admin check instead of both passing it.
-- name: CountAdminsForUpdate :one
SELECT count(*) FROM (
    SELECT id FROM users WHERE role = 'admin' FOR UPDATE
) admins;

-- name: DeleteUserByEmail :execrows
DELETE FROM users WHERE lower(email) = lower($1);

-- name: DeleteUserByID :execrows
DELETE FROM users WHERE id = $1;
