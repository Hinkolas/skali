-- +goose Up

-- Promotions record their source environment on the deployment (the durable
-- record), giving revisions provenance and letting the console preselect
-- the environment a source was last promoted to. NULL for direct deploys.
ALTER TABLE deployments ADD COLUMN from_environment_id UUID REFERENCES environments(id) ON DELETE SET NULL;

-- +goose Down

ALTER TABLE deployments DROP COLUMN from_environment_id;
