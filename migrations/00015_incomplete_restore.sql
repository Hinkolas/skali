-- +goose Up

-- The restore hold: a restore that stopped the environment and has not
-- completed may have left partial data behind, so nothing may bring the
-- environment back until a restore completes or someone accepts the data
-- as it is. The hold names the restore and the components it set out to
-- rewrite; a later restore must rewrite all of them to lift it. It is set
-- in the statement that stops the environment and cleared in the one that
-- resumes it, so a daemon restart in between leaves it in place.
ALTER TABLE environment_targets
    ADD COLUMN incomplete_restore_id UUID REFERENCES backups (id),
    ADD COLUMN incomplete_restore_components TEXT[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE environment_targets
    DROP COLUMN incomplete_restore_components,
    DROP COLUMN incomplete_restore_id;
