-- +goose Up

-- Value pruning (skali deploy --prune-values): the deployment removes the
-- environment's stored values the definition no longer references, inside
-- the promotion transaction. The request travels on the deployment row so
-- completion re-reads it instead of trusting the client a second time.
ALTER TABLE deployments ADD COLUMN prune_values BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down

ALTER TABLE deployments DROP COLUMN prune_values;
