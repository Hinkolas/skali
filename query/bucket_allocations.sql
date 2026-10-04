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

-- Publication commits what the output mirror now holds, only after the
-- mirror holds it: the endpoint, and an output_version bump when the
-- mirror changed or the endpoint moved (the bump rolls the consumers).
-- The first confirmation after an upgrade only stamps the time.
-- name: PublishBucketAllocationOutputs :one
UPDATE bucket_allocations
SET endpoint = sqlc.arg(endpoint),
    output_version = output_version
        + CASE WHEN sqlc.arg(changed)::boolean OR endpoint <> sqlc.arg(endpoint) THEN 1 ELSE 0 END,
    outputs_published_at = CASE
        WHEN sqlc.arg(changed)::boolean OR endpoint <> sqlc.arg(endpoint) OR outputs_published_at IS NULL THEN now()
        ELSE outputs_published_at
    END
WHERE id = sqlc.arg(id) AND released_at IS NULL
RETURNING *;

-- The legacy-edge gate: how many live allocations have not confirmed
-- their mirror yet, and when the last one changed.
-- name: BucketOutputPublication :one
SELECT count(*) FILTER (WHERE outputs_published_at IS NULL)::bigint AS unpublished,
       coalesce(max(outputs_published_at), 'epoch'::timestamptz)::timestamptz AS last_published_at
FROM bucket_allocations
WHERE released_at IS NULL;

-- Rotation bookkeeping. Begin commits the keypair the credential Secret
-- already holds: the new access key id, the consumer-visible version bump,
-- and the instant the previous keypair retires, in one statement and
-- exactly once per key (the guard makes a repeated pass a no-op). Secret
-- keys never enter rows.
-- name: BeginBucketAllocationCredentialRotation :execrows
UPDATE bucket_allocations
SET access_key_id = $2,
    credential_version = credential_version + 1,
    credential_retire_at = $3
WHERE id = $1 AND released_at IS NULL AND access_key_id <> $2;

-- Finish clears the overlap once the previous keypair is retired.
-- name: FinishBucketAllocationCredentialRotation :execrows
UPDATE bucket_allocations
SET credential_retire_at = NULL
WHERE id = $1 AND credential_retire_at IS NOT NULL;

-- The restore fence (see migration 00007): set while a restore rewrites
-- the bucket, cleared when it completes or the environment moves on.
-- name: FenceBucketAllocation :execrows
UPDATE bucket_allocations
SET fenced_at = now()
WHERE id = $1 AND released_at IS NULL AND fenced_at IS NULL;

-- name: UnfenceBucketAllocation :execrows
UPDATE bucket_allocations
SET fenced_at = NULL
WHERE id = $1 AND fenced_at IS NOT NULL;
