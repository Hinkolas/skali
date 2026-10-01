-- +goose Up

-- The bucket's declared public hostname (the compiled buckets.<key>.route
-- block with its domain resolved and canonical, as JSON); NULL keeps the
-- bucket in-cluster. The substrate publishes it as the bucket's endpoint
-- and the environment renderer serves it at the edge.
ALTER TABLE bucket_claims ADD COLUMN route JSONB;

-- +goose Down
ALTER TABLE bucket_claims DROP COLUMN route;
