-- name: CreateSession :one
INSERT INTO sessions (id, user_id, token_hash, expires_at, ip_address, user_agent)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetSessionAndUserByTokenHash :one
SELECT sqlc.embed(sessions), sqlc.embed(users)
FROM sessions
JOIN users ON users.id = sessions.user_id
WHERE sessions.token_hash = $1;

-- name: ExtendSession :exec
UPDATE sessions SET expires_at = $2, updated_at = now() WHERE id = $1;

-- name: DeleteSessionByTokenHash :execrows
DELETE FROM sessions WHERE token_hash = $1;

-- name: DeleteSessionByID :execrows
DELETE FROM sessions WHERE id = $1 AND user_id = $2;

-- name: ListSessionsByUser :many
SELECT * FROM sessions
WHERE user_id = $1 AND expires_at > now()
ORDER BY created_at DESC;

-- name: DeleteUserSessionsExcept :execrows
DELETE FROM sessions WHERE user_id = $1 AND id <> $2;

-- name: DeleteSessionsByUser :execrows
DELETE FROM sessions WHERE user_id = $1;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE expires_at <= now();
