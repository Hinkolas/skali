-- name: CreateDatabaseClaim :one
INSERT INTO database_claims (
    id, owner_kind, project_id, environment_id, service_key, system_key,
    owner_ref, engine, major, isolation, availability,
    storage_bytes, extensions, pitr_seconds
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
RETURNING *;

-- name: GetDatabaseClaim :one
SELECT * FROM database_claims WHERE id = $1;

-- name: GetDatabaseClaimForUpdate :one
SELECT * FROM database_claims WHERE id = $1 FOR UPDATE;

-- name: GetLiveServiceDatabaseClaim :one
SELECT * FROM database_claims
WHERE environment_id = $1 AND service_key = $2
  AND owner_kind = 'service' AND phase <> 'released';

-- name: GetLiveSystemDatabaseClaim :one
SELECT * FROM database_claims
WHERE system_key = $1 AND owner_kind = 'system' AND phase <> 'released';

-- name: ListLiveDatabaseClaimsByEnvironment :many
SELECT * FROM database_claims
WHERE environment_id = $1 AND owner_kind = 'service' AND phase <> 'released'
ORDER BY service_key;

-- Every live claim, for rebuilding the observed claim projections on boot.
-- name: ListLiveDatabaseClaims :many
SELECT * FROM database_claims
WHERE phase <> 'released'
ORDER BY created_at;

-- Claims with reconciliation work outstanding; the substrate boot pass
-- enqueues these instead of trusting memory.
-- name: ListUnsettledDatabaseClaims :many
SELECT * FROM database_claims
WHERE phase IN ('pending', 'bound', 'releasing')
ORDER BY created_at;

-- Compare-and-swap phase change guarded by internal/claim.Phases in the
-- domain service; the WHERE guard turns a lost race into a 0-row no-op.
-- name: SetDatabaseClaimPhase :execrows
UPDATE database_claims
SET phase = sqlc.arg(to_phase), updated_at = now()
WHERE id = $1 AND phase = sqlc.arg(from_phase);

-- Mutable spec fields only. Engine, major, and isolation identify the claim's
-- physical home; changing them is a destructive replacement, never an update.
-- name: SetDatabaseClaimSpec :execrows
UPDATE database_claims
SET availability = $2, storage_bytes = $3, extensions = $4,
    pitr_seconds = $5, updated_at = now()
WHERE id = $1;

-- The local-dev pool lifecycle input (owner decision 2026-07-29): a pool may
-- hibernate when no live claim on it belongs to an environment whose target
-- state is active. System claims have no environment and always count as
-- active. Counts bound and provisioned claims: a claim mid-provisioning must
-- keep the pool awake.
-- name: CountActiveDatabaseClaimsByCluster :one
SELECT count(*) FROM database_claims c
JOIN database_placements p ON p.claim_id = c.id AND p.superseded_at IS NULL
LEFT JOIN environment_targets t ON t.environment_id = c.environment_id
WHERE p.cluster_id = $1
  AND c.phase IN ('bound', 'provisioned')
  AND (c.owner_kind = 'system' OR t.state = 'active');

-- Live claims placed on a cluster, for managed-role rendering and GC checks.
-- name: ListLiveDatabaseClaimsByCluster :many
SELECT c.* FROM database_claims c
JOIN database_placements p ON p.claim_id = c.id AND p.superseded_at IS NULL
WHERE p.cluster_id = $1 AND c.phase <> 'released'
ORDER BY c.created_at;
