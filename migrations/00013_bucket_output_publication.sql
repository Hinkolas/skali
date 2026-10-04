-- +goose Up

-- The bucket output mirror is the Secret consumers read; the row records
-- what it holds. output_version advances each time the mirror materially
-- changes (an endpoint republished, a keypair written) and is folded into
-- the consumers' pod-template identity, so they roll exactly when the
-- values they read change. outputs_published_at is when that last
-- happened; NULL means the mirror has not been confirmed since this
-- migration, which gates the removal of the retired installation-wide S3
-- edge until every consumer has had time to roll off it.
ALTER TABLE bucket_allocations
    ADD COLUMN output_version BIGINT NOT NULL DEFAULT 1,
    ADD COLUMN outputs_published_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE bucket_allocations
    DROP COLUMN output_version,
    DROP COLUMN outputs_published_at;
