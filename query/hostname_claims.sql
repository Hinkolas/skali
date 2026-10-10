-- name: GetHostnameClaim :one
SELECT * FROM hostname_claims WHERE hostname = $1;

-- name: ListEnvironmentHostnames :many
SELECT * FROM hostname_claims WHERE environment_id = $1 ORDER BY hostname;

-- ClaimEnvironmentHostnames points the environment's hostname claims at
-- a revision in one statement: its claims on hostnames the revision does
-- not route retire, and each hostname it routes is claimed for it unless
-- another environment holds it or the installation reserves it. hostnames
-- must be distinct; the ones that could not be claimed return.
-- name: ClaimEnvironmentHostnames :many
WITH retired AS (
    UPDATE hostname_claims SET target_revision_id = NULL
    WHERE hostname_claims.environment_id = sqlc.arg(environment_id)::uuid
      AND NOT (hostname_claims.hostname = ANY(sqlc.arg(hostnames)::text[]))
), claimed AS (
    INSERT INTO hostname_claims (hostname, environment_id, target_revision_id)
    SELECT hostname, sqlc.arg(environment_id)::uuid, sqlc.arg(target_revision_id)::uuid
    FROM unnest(sqlc.arg(hostnames)::text[]) AS hostname
    ON CONFLICT (hostname) DO UPDATE SET target_revision_id = EXCLUDED.target_revision_id
    WHERE hostname_claims.environment_id = EXCLUDED.environment_id AND NOT hostname_claims.reserved
    RETURNING hostname_claims.hostname
)
SELECT hostname::text FROM unnest(sqlc.arg(hostnames)::text[]) AS hostname
WHERE hostname NOT IN (SELECT claimed.hostname FROM claimed);

-- name: RetireEnvironmentHostnames :exec
UPDATE hostname_claims SET target_revision_id = NULL WHERE environment_id = $1;

-- name: DeleteRetiredHostname :exec
DELETE FROM hostname_claims WHERE hostname = $1 AND environment_id = $2 AND target_revision_id IS NULL;

-- name: ReserveHostname :execrows
INSERT INTO hostname_claims (hostname, reserved) VALUES ($1, true)
ON CONFLICT (hostname) DO UPDATE SET reserved = true WHERE hostname_claims.reserved;

-- name: ReleaseReservedHostnamesExcept :many
DELETE FROM hostname_claims
WHERE reserved AND NOT (hostname = ANY($1::text[]))
RETURNING hostname;
