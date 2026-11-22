-- +goose Up

-- The SeaweedFS volume size limit the masters run with, in megabytes
-- (-volumeSizeLimitMB). NULL is the platform default. Every bucket is its
-- own collection and holds at least one volume per replica, and volume
-- servers derive their slot count from free disk divided by this limit,
-- so it sets how many buckets the store can hold. An admin setting:
-- changing it rolls the masters, and the volume servers pick the new
-- limit up from their next heartbeat.
ALTER TABLE object_stores ADD COLUMN volume_size_limit_mb INT
    CHECK (volume_size_limit_mb IS NULL OR volume_size_limit_mb > 0);

-- +goose Down
ALTER TABLE object_stores DROP COLUMN volume_size_limit_mb;
