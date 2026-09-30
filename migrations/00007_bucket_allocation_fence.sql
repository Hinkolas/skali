-- +goose Up

-- The restore fence: while set, the bucket's own identity is deleted so
-- every credential the environment holds (mirrored keys, presigned URLs)
-- is refused, and the platform identity alone writes the restored
-- contents. Provisioning leaves the identity absent while fenced; the
-- probe lifts a fence whose environment is no longer down (a restore that
-- failed and was abandoned for a deploy).
ALTER TABLE bucket_allocations ADD COLUMN fenced_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE bucket_allocations DROP COLUMN fenced_at;
