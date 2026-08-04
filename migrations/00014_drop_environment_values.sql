-- +goose Up

-- Pre-release wipe: the plain/secret value split is gone and
-- environment_secrets is the single, encrypted store. Existing plain values
-- are dropped; environments re-upload them from an environment file on the
-- next deployment.
DROP TABLE environment_values;

-- +goose Down

-- Recreate the 00005 table shape, empty, so the down path stays runnable.
CREATE TABLE environment_values (
    id             UUID PRIMARY KEY,
    environment_id UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    name           TEXT NOT NULL,
    version        BIGINT NOT NULL,
    value          TEXT NOT NULL,
    state          TEXT NOT NULL DEFAULT 'staged'
                   CHECK (state IN ('staged', 'current', 'superseded')),
    candidate_id   UUID,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (environment_id, name, version)
);
CREATE UNIQUE INDEX environment_values_current_key
    ON environment_values (environment_id, name) WHERE state = 'current';
CREATE INDEX environment_values_candidate_id_idx ON environment_values (candidate_id);
