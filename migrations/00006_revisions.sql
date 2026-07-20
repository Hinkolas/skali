-- +goose Up

-- Durable artifact records: the verified, content-addressed results of
-- builds and imports (phase machine in internal/artifact). project_id is
-- nullable on purpose: cache artifacts are installation-scoped, and records
-- outlive their project for provenance history.
CREATE TABLE artifacts (
    id           UUID PRIMARY KEY,
    project_id   UUID REFERENCES projects(id) ON DELETE SET NULL,
    application  TEXT NOT NULL DEFAULT '',
    kind         TEXT NOT NULL CHECK (kind IN ('import', 'build-local', 'build-cloud')),
    phase        TEXT NOT NULL DEFAULT 'pending'
                 CHECK (phase IN ('pending', 'verified', 'abandoned', 'evicted')),
    reference    TEXT NOT NULL DEFAULT '',
    digest       TEXT CHECK (digest IS NULL OR digest ~ '^sha256:[0-9a-f]{64}$'),
    upstream     TEXT NOT NULL DEFAULT '',
    context_hash TEXT NOT NULL DEFAULT '',
    provenance   JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    verified_at  TIMESTAMPTZ,
    -- Content that was ever verified keeps its digest forever.
    CHECK (phase NOT IN ('verified', 'evicted') OR digest IS NOT NULL)
);
CREATE INDEX artifacts_project_id_idx ON artifacts (project_id);
CREATE INDEX artifacts_digest_idx ON artifacts (digest);

-- Immutable revisions. document is the complete canonical revision.Revision
-- JSON, which never contains secret plaintext by construction; the other
-- columns are denormalized for queries only. Identical inputs dedupe per
-- environment by checksum.
CREATE TABLE revisions (
    id                    UUID PRIMARY KEY,
    project_id            UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    environment_id        UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    definition_version_id UUID NOT NULL REFERENCES definition_versions(id),
    schema_version        TEXT NOT NULL,
    checksum              TEXT NOT NULL,
    definition_hash       TEXT NOT NULL,
    values_hash           TEXT NOT NULL,
    compiler_version      TEXT NOT NULL,
    document              JSONB NOT NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (environment_id, checksum)
);
CREATE INDEX revisions_environment_id_idx ON revisions (environment_id, created_at DESC);

-- Retention leases: a revision leases every artifact it references, and a
-- leased artifact cannot be evicted. Dropping a revision releases its
-- leases through the cascade.
CREATE TABLE artifact_leases (
    revision_id UUID NOT NULL REFERENCES revisions(id) ON DELETE CASCADE,
    artifact_id UUID NOT NULL REFERENCES artifacts(id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (revision_id, artifact_id)
);
CREATE INDEX artifact_leases_artifact_id_idx ON artifact_leases (artifact_id);

-- Target and last-active revision pointers, one row per environment. A
-- separate table avoids a circular environments <-> revisions foreign key.
-- Deployment promotion is the only writer of target_revision_id; activation
-- (R2 reconciliation) will own active_revision_id. A daemon restart never
-- writes this table.
CREATE TABLE environment_targets (
    environment_id     UUID PRIMARY KEY REFERENCES environments(id) ON DELETE CASCADE,
    target_revision_id UUID REFERENCES revisions(id),
    active_revision_id UUID REFERENCES revisions(id),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Environments created before this migration get their pointer row now;
-- environment creation inserts it from here on.
INSERT INTO environment_targets (environment_id)
SELECT id FROM environments;

-- +goose Down
DROP TABLE environment_targets;
DROP TABLE artifact_leases;
DROP TABLE revisions;
DROP TABLE artifacts;
