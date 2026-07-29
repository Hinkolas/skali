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
