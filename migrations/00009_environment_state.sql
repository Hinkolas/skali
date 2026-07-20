-- +goose Up

-- Desired presence of an environment's runtime state. Deletion is explicit
-- desired absence with a persisted destructive decision, never an implicit
-- side effect of a row disappearing. 'active' is the normal state; 'down'
-- removes runtime workloads but keeps the namespace, volumes, values, and
-- history; 'releasing' is the one-way purge decision that ends with the
-- namespace and the environment row gone.
ALTER TABLE environment_targets
    ADD COLUMN state TEXT NOT NULL DEFAULT 'active'
    CHECK (state IN ('active', 'down', 'releasing'));

-- +goose Down
ALTER TABLE environment_targets DROP COLUMN state;
