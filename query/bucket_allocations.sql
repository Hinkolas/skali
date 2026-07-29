-- name: CreateBucketAllocation :one
INSERT INTO bucket_allocations (
    id, claim_id, store_id, bucket_name, access_key_id,
    credential_secret, endpoint, region
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetLiveBucketAllocationByClaim :one
SELECT * FROM bucket_allocations
WHERE claim_id = $1 AND released_at IS NULL;

-- name: ListLiveBucketAllocationsByStore :many
SELECT * FROM bucket_allocations
WHERE store_id = $1 AND released_at IS NULL
ORDER BY bucket_name;

-- name: CountLiveBucketAllocationsByStore :one
SELECT count(*) FROM bucket_allocations
WHERE store_id = $1 AND released_at IS NULL;

-- name: ReleaseBucketAllocation :execrows
UPDATE bucket_allocations
SET released_at = now()
WHERE id = $1 AND released_at IS NULL;

-- The endpoint is republished when the installation gains or loses an
-- external S3 domain; consumers roll through the mirror Secret change.
-- name: SetBucketAllocationEndpoint :execrows
UPDATE bucket_allocations
SET endpoint = $2
WHERE id = $1 AND released_at IS NULL;

-- Rotation bookkeeping: the version is the consumer-visible change signal;
-- the new secret key itself lives only in the credential Secret.
-- name: BumpBucketAllocationCredentialVersion :execrows
UPDATE bucket_allocations
SET credential_version = credential_version + 1
WHERE id = $1 AND released_at IS NULL;
