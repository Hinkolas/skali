-- name: CreateDatabaseTenant :one
INSERT INTO database_tenants (
    id, claim_id, cluster_id, database_name, role_name,
    credential_secret, host, port
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetLiveDatabaseTenantByClaim :one
SELECT * FROM database_tenants
WHERE claim_id = $1 AND released_at IS NULL;

-- name: ListLiveDatabaseTenantsByCluster :many
SELECT * FROM database_tenants
WHERE cluster_id = $1 AND released_at IS NULL
ORDER BY role_name;

-- name: CountLiveDatabaseTenantsByCluster :one
SELECT count(*) FROM database_tenants
WHERE cluster_id = $1 AND released_at IS NULL;

-- name: ReleaseDatabaseTenant :execrows
UPDATE database_tenants
SET released_at = now()
WHERE id = $1 AND released_at IS NULL;

-- Rotation bookkeeping: the version is the consumer-visible change signal;
-- the new password itself lives only in the credential Secret.
-- name: BumpDatabaseTenantCredentialVersion :execrows
UPDATE database_tenants
SET credential_version = credential_version + 1
WHERE id = $1 AND released_at IS NULL;

-- Every live tenant on a cluster with its claim, the owning project and
-- environment (NULL for system claims), and the newest database storage
-- sample within the cutoff: the pool page's database list in one query.
-- Service-owned tenants sort first, then by project, environment and key.
-- name: ListLiveDatabaseTenantDetailsByCluster :many
SELECT t.id AS tenant_id, t.database_name, t.role_name, t.created_at,
       c.id AS claim_id, c.owner_kind, c.system_key, c.service_key, c.phase, c.storage_bytes,
       c.project_id, c.environment_id,
       p.name AS project_name, p.display_name AS project_display_name,
       e.name AS environment_name,
       s.used_bytes
FROM database_tenants t
JOIN database_claims c ON c.id = t.claim_id
LEFT JOIN projects p ON p.id = c.project_id
LEFT JOIN environments e ON e.id = c.environment_id
LEFT JOIN LATERAL (
    SELECT m.used_bytes
    FROM metric_storage_samples m
    WHERE m.environment_id = c.environment_id
      AND m.service_key = 'databases.' || c.service_key
      AND m.kind = 'database'
      AND m.sampled_at >= sqlc.arg(since)::timestamptz
    ORDER BY m.sampled_at DESC
    LIMIT 1
) s ON true
WHERE t.cluster_id = $1 AND t.released_at IS NULL
ORDER BY (c.owner_kind = 'system'), p.name, e.name, c.service_key, c.system_key;
