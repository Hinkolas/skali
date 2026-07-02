-- name: CreateAccount :one
INSERT INTO accounts (id, user_id, provider_id, password)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetCredentialAccount :one
SELECT * FROM accounts WHERE user_id = $1 AND provider_id = 'credential';

-- name: UpdateAccountPassword :exec
UPDATE accounts SET password = $2, updated_at = now()
WHERE user_id = $1 AND provider_id = 'credential';
