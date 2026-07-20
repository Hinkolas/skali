-- +goose Up

-- Typed environment values, append-only per-name versions. Candidate staging
-- lives in the same table: rows move staged -> current -> superseded, and a
-- failed candidate is deleted while still staged. Promoted rows are never
-- deleted; old revisions reference (name, version) and rollback must still
-- resolve them.
CREATE TABLE environment_values (
    id             UUID PRIMARY KEY,
    environment_id UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    name           TEXT NOT NULL,
    version        BIGINT NOT NULL,
    value          TEXT NOT NULL,
    state          TEXT NOT NULL DEFAULT 'staged'
                   CHECK (state IN ('staged', 'current', 'superseded')),
    -- Staging batch marker, deliberately not an FK: the candidate is a
    -- transient grouping, not a durable entity.
    candidate_id   UUID,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (environment_id, name, version)
);
CREATE UNIQUE INDEX environment_values_current_key
    ON environment_values (environment_id, name) WHERE state = 'current';
CREATE INDEX environment_values_candidate_id_idx ON environment_values (candidate_id);

-- Secret values: identical lifecycle, ciphertext instead of plaintext.
-- ciphertext is AES-256-GCM (version byte || nonce || ciphertext) under a
-- key derived from AUTH_SECRET; plaintext exists only in memory.
CREATE TABLE environment_secrets (
    id             UUID PRIMARY KEY,
    environment_id UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    name           TEXT NOT NULL,
    version        BIGINT NOT NULL,
    ciphertext     BYTEA NOT NULL,
    state          TEXT NOT NULL DEFAULT 'staged'
                   CHECK (state IN ('staged', 'current', 'superseded')),
    candidate_id   UUID,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (environment_id, name, version)
);
CREATE UNIQUE INDEX environment_secrets_current_key
    ON environment_secrets (environment_id, name) WHERE state = 'current';
CREATE INDEX environment_secrets_candidate_id_idx ON environment_secrets (candidate_id);

-- +goose Down
DROP TABLE environment_secrets;
DROP TABLE environment_values;
