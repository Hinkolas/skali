-- +goose Up
-- Names can change, identities and backup locations cannot. Existing
-- installations retain their name-based S3 paths; new environments use
-- their UUID. Old names remain aliases while the environment exists.
ALTER TABLE environments
    ADD COLUMN previous_names TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN backup_namespace TEXT NOT NULL DEFAULT '';
UPDATE environments SET backup_namespace = name;

-- Freeze the location in accepted operations too, including operations
-- accepted by the preceding daemon during rollout overlap.
ALTER TABLE backups ADD COLUMN environment_namespace TEXT NOT NULL DEFAULT '';
UPDATE backups SET environment_namespace = environment_name;

-- +goose Down
ALTER TABLE backups DROP COLUMN environment_namespace;
ALTER TABLE environments DROP COLUMN backup_namespace, DROP COLUMN previous_names;
