-- name: GetHostnameClaim :one
SELECT * FROM hostname_claims WHERE hostname = $1;

-- name: ListEnvironmentHostnames :many
SELECT * FROM hostname_claims WHERE environment_id = $1 ORDER BY hostname;

-- name: AcquireHostname :execrows
INSERT INTO hostname_claims (hostname, environment_id, target_revision_id)
VALUES ($1, $2, $3)
ON CONFLICT (hostname) DO UPDATE SET target_revision_id = EXCLUDED.target_revision_id
WHERE hostname_claims.environment_id = EXCLUDED.environment_id AND NOT hostname_claims.reserved;

-- name: RetireEnvironmentHostnames :exec
UPDATE hostname_claims SET target_revision_id = NULL WHERE environment_id = $1;

-- name: DeleteRetiredHostname :exec
DELETE FROM hostname_claims WHERE hostname = $1 AND environment_id = $2 AND target_revision_id IS NULL;

-- name: ReserveHostname :execrows
INSERT INTO hostname_claims (hostname, reserved) VALUES ($1, true)
ON CONFLICT (hostname) DO UPDATE SET reserved = true WHERE hostname_claims.reserved;
