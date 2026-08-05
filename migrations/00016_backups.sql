-- +goose Up

-- The R5 backup skeleton was never wired (zero callers outside the store
-- package). Backups are designed once as a unified environment-level system
-- covering databases, buckets, and volumes together, so the claim-scoped
-- table goes away in favor of the tables below.
DROP TABLE database_backups;

-- Admin-configured external S3 backup locations. Multi-row by design so a
-- later slice can add several targets (onsite plus cloud); the v1 API
-- manages exactly the row named 'default'. The secret access key is sealed
-- by the daemon's crypt key and never leaves it: reads return every field
-- except the secret, which is write-only like environment values.
CREATE TABLE backup_targets (
    id                UUID PRIMARY KEY,
    name              TEXT NOT NULL UNIQUE CHECK (name ~ '^[a-z][a-z0-9-]{0,62}$'),
    endpoint          TEXT NOT NULL,
    region            TEXT NOT NULL DEFAULT '',
    bucket            TEXT NOT NULL,
    prefix            TEXT NOT NULL DEFAULT '',
    access_key_id     TEXT NOT NULL,
    secret_access_key BYTEA NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One row per backup or restore operation. The row drives execution: the
-- backup controller works pending rows, recovery on boot fails rows a dead
-- daemon left running, and the row survives journal run pruning (run_id is
-- a soft link, no FK, because runs are retained per environment and aged
-- out). Durable snapshot history lives in the S3 manifests themselves, not
-- here: a fresh installation lists snapshots by reading the bucket. The
-- project and environment names are denormalized because S3 keys use names,
-- and the names must survive environment deletion for the error surface.
CREATE TABLE backups (
    id               UUID PRIMARY KEY,
    kind             TEXT NOT NULL CHECK (kind IN ('backup', 'restore')),
    environment_id   UUID NOT NULL REFERENCES environments (id) ON DELETE CASCADE,
    project_name     TEXT NOT NULL,
    environment_name TEXT NOT NULL,
    status           TEXT NOT NULL DEFAULT 'pending'
                     CHECK (status IN ('pending', 'running', 'succeeded', 'failed')),
    snapshot_key     TEXT NOT NULL DEFAULT '',
    revision_id      UUID,
    run_id           UUID,
    error            TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at      TIMESTAMPTZ
);
CREATE INDEX backups_environment_idx ON backups (environment_id, created_at DESC);

-- +goose Down
DROP TABLE backups;
DROP TABLE backup_targets;
CREATE TABLE database_backups (
    id          UUID PRIMARY KEY,
    claim_id    UUID,
    cluster_id  UUID,
    kind        TEXT NOT NULL CHECK (kind IN ('scheduled', 'manual', 'restore')),
    status      TEXT NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending', 'running', 'succeeded', 'failed')),
    run_id      UUID,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ
);
CREATE INDEX database_backups_claim_idx ON database_backups (claim_id, created_at DESC);
