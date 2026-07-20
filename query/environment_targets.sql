-- name: CreateEnvironmentTarget :exec
INSERT INTO environment_targets (environment_id)
VALUES ($1);

-- name: GetEnvironmentTarget :one
SELECT * FROM environment_targets WHERE environment_id = $1;

-- The single writer of the target pointer; runs only inside the deploy
-- promotion or rollback transaction.
-- name: SetEnvironmentTarget :execrows
UPDATE environment_targets
SET target_revision_id = $2, updated_at = now()
WHERE environment_id = $1;
