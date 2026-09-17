-- +goose Up

-- Pool tuning. memory_bytes is the pool's memory budget; NULL means the
-- substrate sizes it from the smallest database-capable node. parameters
-- holds the administrator's PostgreSQL parameter overrides (name -> value in
-- PostgreSQL syntax) layered on top of the budget-derived set; an empty
-- object means "derived only".
ALTER TABLE database_clusters
    ADD COLUMN memory_bytes BIGINT,
    ADD COLUMN parameters JSONB NOT NULL DEFAULT '{}'::jsonb;

-- +goose Down
ALTER TABLE database_clusters
    DROP COLUMN parameters,
    DROP COLUMN memory_bytes;
