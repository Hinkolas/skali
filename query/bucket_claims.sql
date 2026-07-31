-- name: CreateBucketClaim :one
INSERT INTO bucket_claims (
    id, owner_kind, project_id, environment_id, service_key, system_key,
    owner_ref, visibility, storage_quota_bytes, object_quota,
    max_object_bytes, versioning, abort_uploads_after_seconds,
    expire_noncurrent_after_seconds
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
RETURNING *;

-- name: GetBucketClaim :one
SELECT * FROM bucket_claims WHERE id = $1;

-- name: GetBucketClaimForUpdate :one
SELECT * FROM bucket_claims WHERE id = $1 FOR UPDATE;

-- name: GetLiveServiceBucketClaim :one
SELECT * FROM bucket_claims
WHERE environment_id = $1 AND service_key = $2
  AND owner_kind = 'service' AND phase <> 'released';

-- name: GetLiveSystemBucketClaim :one
SELECT * FROM bucket_claims
WHERE system_key = $1 AND owner_kind = 'system' AND phase <> 'released';

-- name: ListLiveBucketClaimsByEnvironment :many
SELECT * FROM bucket_claims
WHERE environment_id = $1 AND owner_kind = 'service' AND phase <> 'released'
ORDER BY service_key;

-- Every live claim, for rebuilding the observed claim projections on boot.
-- name: ListLiveBucketClaims :many
SELECT * FROM bucket_claims
WHERE phase <> 'released'
ORDER BY created_at;

-- Claims with reconciliation work outstanding; the substrate boot pass
-- enqueues these instead of trusting memory.
-- name: ListUnsettledBucketClaims :many
SELECT * FROM bucket_claims
WHERE phase IN ('pending', 'bound', 'releasing')
ORDER BY created_at;

-- Compare-and-swap phase change guarded by internal/claim.Phases in the
-- domain service; the WHERE guard turns a lost race into a 0-row no-op.
-- name: SetBucketClaimPhase :execrows
UPDATE bucket_claims
SET phase = sqlc.arg(to_phase), updated_at = now()
WHERE id = $1 AND phase = sqlc.arg(from_phase);

-- Mutable spec fields only. Visibility and versioning change the bucket's
-- externally observable contract; changing them is a destructive replacement
-- decided at plan level, never an in-place update.
-- name: SetBucketClaimSpec :execrows
UPDATE bucket_claims
SET storage_quota_bytes = $2, object_quota = $3, max_object_bytes = $4,
    abort_uploads_after_seconds = $5, expire_noncurrent_after_seconds = $6,
    updated_at = now()
WHERE id = $1;

-- Live claims allocated on a store, for identity rendering and GC checks.
-- name: ListLiveBucketClaimsByStore :many
SELECT c.* FROM bucket_claims c
JOIN bucket_allocations a ON a.claim_id = c.id AND a.released_at IS NULL
WHERE a.store_id = $1 AND c.phase <> 'released'
ORDER BY c.created_at;
