-- +goose Up

-- A project delete is a persisted decision, like an environment purge: the
-- row stays, marked, while the reconciler purges every environment, and the
-- purge of the last environment deletes the project row in the same
-- transaction. A marked project accepts no new environments or deployments.
ALTER TABLE projects ADD COLUMN deleting_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE projects DROP COLUMN deleting_at;
