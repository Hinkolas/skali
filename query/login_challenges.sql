-- name: CreateLoginChallenge :one
INSERT INTO login_challenges (id, user_id, token_hash, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetLoginChallengeByTokenHash :one
SELECT * FROM login_challenges WHERE token_hash = $1 AND expires_at > now();

-- name: IncrementLoginChallengeAttempts :one
UPDATE login_challenges SET attempts = attempts + 1
WHERE id = $1
RETURNING attempts;

-- name: DeleteLoginChallenge :exec
DELETE FROM login_challenges WHERE id = $1;

-- name: DeleteExpiredLoginChallenges :execrows
DELETE FROM login_challenges WHERE expires_at <= now();
