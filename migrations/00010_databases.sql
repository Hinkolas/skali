-- +goose Up

-- The shared database substrate: claims,
-- physical clusters, placements, tenants, and backup records. These tables are
-- authoritative desired/durable state below product services; live CNPG
-- topology (instances, primaries, readiness) is never mirrored here, it
-- belongs to the observed store. The installer-owned bootstrap cluster
-- (skali-db in skali-system) has no row in any of these tables and can never
-- acquire one: the substrate only places onto rows it created itself.

-- A physical database runtime, initially one CNPG Cluster in skali-platform,
-- owned and applied by skalid under the skalid-platform field manager. class
-- records the packing policy that created it: 'shared' pools accept shared
-- claims per (engine, major), 'environment' pools carry one environment's
-- project-isolated claims, 'dedicated' pools carry exactly one claim. Owner
-- references are soft (plain UUIDs, no foreign key) so released pools keep
-- their history after the owning environment or claim row is gone.
-- 'hibernated' is the local-dev idle state: workloads stopped via CNPG
-- hibernation with data retained; production pools never hibernate.
CREATE TABLE database_clusters (
    id             UUID PRIMARY KEY,
    -- CNPG Cluster object name in skali-platform; also the connection-host
    -- prefix (<name>-rw.skali-platform.svc).
    name           TEXT NOT NULL CHECK (name ~ '^[a-z][a-z0-9-]{0,62}$'),
    engine         TEXT NOT NULL,
    major          INT  NOT NULL,
    class          TEXT NOT NULL CHECK (class IN ('shared', 'environment', 'dedicated')),
    environment_id UUID,
    claim_id       UUID,
    -- Desired shape: instance count from the availability tier and requested
    -- storage. The observed store reports what actually runs.
    instances      INT    NOT NULL,
    storage_bytes  BIGINT NOT NULL,
    -- Blessed image from the engine catalog at creation time; a catalog
    -- change never rewrites existing pools implicitly.
    image          TEXT NOT NULL,
    state          TEXT NOT NULL DEFAULT 'active'
                   CHECK (state IN ('active', 'hibernated', 'releasing', 'released')),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX database_clusters_name_key
    ON database_clusters (name) WHERE state <> 'released';
-- Dumb packing v1: at most one live shared pool per (engine, major).
CREATE UNIQUE INDEX database_clusters_shared_key
    ON database_clusters (engine, major) WHERE class = 'shared' AND state <> 'released';
-- One live environment pool per (engine, major, environment).
CREATE UNIQUE INDEX database_clusters_environment_key
    ON database_clusters (engine, major, environment_id)
    WHERE class = 'environment' AND state <> 'released';
-- One live dedicated pool per claim.
CREATE UNIQUE INDEX database_clusters_claim_key
    ON database_clusters (claim_id) WHERE class = 'dedicated' AND state <> 'released';

-- A requested database capability (internal/claim phase machine). User and
-- system claims are one table and one code path; owner_kind is data, never a
-- branch. Owner references are soft and additionally denormalized into
-- owner_ref ('project/<p>/environment/<e>/service/<k>' or 'system/<k>') so a
-- released claim keeps a readable identity after the owning environment row
-- is purged: identity is retained for history. There is no failed phase; an
-- unsatisfiable claim stays pending with a visible waiting condition.
CREATE TABLE database_claims (
    id             UUID PRIMARY KEY,
    owner_kind     TEXT NOT NULL CHECK (owner_kind IN ('service', 'system')),
    project_id     UUID,
    environment_id UUID,
    service_key    TEXT NOT NULL DEFAULT '',
    system_key     TEXT NOT NULL DEFAULT '',
    owner_ref      TEXT NOT NULL,
    engine         TEXT NOT NULL,
    major          INT  NOT NULL,
    isolation      TEXT NOT NULL CHECK (isolation IN ('shared', 'project', 'dedicated')),
    availability   TEXT NOT NULL CHECK (availability IN ('single', 'asynchronous', 'synchronous')),
    storage_bytes  BIGINT NOT NULL DEFAULT 0,
    extensions     JSONB  NOT NULL DEFAULT '[]'::jsonb,
    pitr_seconds   BIGINT NOT NULL DEFAULT 0,
    phase          TEXT NOT NULL DEFAULT 'pending'
                   CHECK (phase IN ('pending', 'bound', 'provisioned', 'releasing', 'released')),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- One live claim per service owner and per system owner.
CREATE UNIQUE INDEX database_claims_service_key
    ON database_claims (environment_id, service_key)
    WHERE owner_kind = 'service' AND phase <> 'released';
CREATE UNIQUE INDEX database_claims_system_key
    ON database_claims (system_key)
    WHERE owner_kind = 'system' AND phase <> 'released';
CREATE INDEX database_claims_environment_idx ON database_claims (environment_id);

-- System-owned assignment from claim to cluster: not part of
-- the portable definition or any revision, because the platform may relocate
-- a tenant without changing user intent. Relocation supersedes rather than
-- rewrites, so placement history explains where a tenant has lived.
CREATE TABLE database_placements (
    id            UUID PRIMARY KEY,
    claim_id      UUID NOT NULL REFERENCES database_claims(id),
    cluster_id    UUID NOT NULL REFERENCES database_clusters(id),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    superseded_at TIMESTAMPTZ
);
-- At most one active placement per claim.
CREATE UNIQUE INDEX database_placements_active_key
    ON database_placements (claim_id) WHERE superseded_at IS NULL;
CREATE INDEX database_placements_cluster_idx ON database_placements (cluster_id);

-- A logical database inside a cluster: generated identity, the NAME of the
-- credential Secret in skali-platform, and the non-secret connection outputs.
-- Credential values live only in Kubernetes Secrets and their environment
-- mirrors, never in rows; credential_version counts
-- rotations so consumers can roll on change.
CREATE TABLE database_tenants (
    id                 UUID PRIMARY KEY,
    claim_id           UUID NOT NULL REFERENCES database_claims(id),
    cluster_id         UUID NOT NULL REFERENCES database_clusters(id),
    database_name      TEXT NOT NULL,
    role_name          TEXT NOT NULL,
    credential_secret  TEXT NOT NULL,
    credential_version BIGINT NOT NULL DEFAULT 1,
    host               TEXT NOT NULL,
    port               INT  NOT NULL DEFAULT 5432,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    released_at        TIMESTAMPTZ
);
-- One live tenant per claim, and unique live identities inside a cluster.
CREATE UNIQUE INDEX database_tenants_claim_key
    ON database_tenants (claim_id) WHERE released_at IS NULL;
CREATE UNIQUE INDEX database_tenants_database_key
    ON database_tenants (cluster_id, database_name) WHERE released_at IS NULL;
CREATE UNIQUE INDEX database_tenants_role_key
    ON database_tenants (cluster_id, role_name) WHERE released_at IS NULL;

-- Backup/restore skeleton (R5): durable linkage between a claim, the run that
-- explains the operation, and its outcome. Production backup policy lands in
-- R8; nothing here drives reconciliation. Soft references throughout: backup
-- history outlives claims, clusters, and pruned runs.
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

-- +goose Down
DROP TABLE database_backups;
DROP TABLE database_tenants;
DROP TABLE database_placements;
DROP TABLE database_claims;
DROP TABLE database_clusters;
