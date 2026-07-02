-- name: CreateUser :one
INSERT INTO users (id, email, name)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE lower(email) = lower($1);

-- name: SetUserTwoFactorEnabled :exec
UPDATE users SET two_factor_enabled = $2, updated_at = now() WHERE id = $1;

-- name: ListUsers :many
SELECT * FROM users ORDER BY created_at;

-- name: DeleteUserByEmail :execrows
DELETE FROM users WHERE lower(email) = lower($1);
