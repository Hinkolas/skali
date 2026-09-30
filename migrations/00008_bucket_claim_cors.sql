-- +goose Up

-- The bucket's declared CORS configuration (the compiled buckets.<key>.cors
-- block as JSON); NULL keeps the store's permissive fallback. Reconciled
-- onto the bucket by the substrate like the rest of its configuration.
ALTER TABLE bucket_claims ADD COLUMN cors JSONB;

-- +goose Down
ALTER TABLE bucket_claims DROP COLUMN cors;
