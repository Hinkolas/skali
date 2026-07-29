-- +goose Up

-- The object-storage substrate (REWORK_V2 sections 10.5, 16 R6): the physical
-- SeaweedFS system, logical bucket claims, and their allocations. Mirrors
-- 00010: these tables are authoritative desired/durable state below product
-- services; live SeaweedFS topology (masters, volumes, usage) is never
-- mirrored here, it belongs to the observed store. The SeaweedFS metadata
-- database is an ordinary system claim in database_claims
-- ('system/object-storage/metadata'), never a table here.

-- The physical SeaweedFS system, rendered and applied by skalid in
-- skali-platform under the skalid-platform field manager. Exactly one live
-- store per installation; the shape columns record desired topology derived
-- from the object-storage node count at creation (the observed store reports
-- what actually runs). 'stopped' is the local-dev idle state: workloads
-- scaled to zero with volumes and metadata retained; production stores never
-- stop.
CREATE TABLE object_stores (
    id                   UUID PRIMARY KEY,
    -- Component-name prefix in skali-platform ("seaweed"); the code always
    -- passes the constant, so the live-name unique below enforces the
    -- single-live-store invariant.
    name                 TEXT NOT NULL CHECK (name ~ '^[a-z][a-z0-9-]{0,62}$'),
    masters              INT  NOT NULL,
    volume_servers       INT  NOT NULL,
    -- SeaweedFS replication code (e.g. '000', '001') chosen from the capable
    -- node count at creation; a topology change never rewrites it implicitly.
    replication          TEXT NOT NULL,
    -- Requested storage per volume server.
    volume_storage_bytes BIGINT NOT NULL,
    -- Blessed image pinned at creation time.
    image                TEXT NOT NULL,
    state                TEXT NOT NULL DEFAULT 'active'
                         CHECK (state IN ('active', 'stopped', 'releasing', 'released')),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX object_stores_name_key
    ON object_stores (name) WHERE state <> 'released';

-- A requested bucket capability (internal/claim phase machine, shared with
-- database claims). User and system claims are one table and one code path;
-- owner_kind is data, never a branch. Owner references are soft and
-- denormalized into owner_ref so a released claim keeps a readable identity
-- after the owning environment row is purged. There is no failed phase; an
-- unsatisfiable claim stays pending with a visible waiting condition. The
-- spec records the full authored surface even where v1 policy rejects a
-- field at deploy open; rejection happens before a claim exists.
CREATE TABLE bucket_claims (
    id                  UUID PRIMARY KEY,
    owner_kind          TEXT NOT NULL CHECK (owner_kind IN ('service', 'system')),
    project_id          UUID,
    environment_id      UUID,
    service_key         TEXT NOT NULL DEFAULT '',
    system_key          TEXT NOT NULL DEFAULT '',
    owner_ref           TEXT NOT NULL,
    visibility          TEXT NOT NULL CHECK (visibility IN ('private', 'public-read')),
    storage_quota_bytes BIGINT NOT NULL DEFAULT 0,
    object_quota        BIGINT NOT NULL DEFAULT 0,
    max_object_bytes    BIGINT NOT NULL DEFAULT 0,
    versioning          TEXT NOT NULL CHECK (versioning IN ('enabled', 'disabled')),
    abort_uploads_after_seconds     BIGINT NOT NULL DEFAULT 0,
    expire_noncurrent_after_seconds BIGINT NOT NULL DEFAULT 0,
    phase               TEXT NOT NULL DEFAULT 'pending'
                        CHECK (phase IN ('pending', 'bound', 'provisioned', 'releasing', 'released')),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- One live claim per service owner and per system owner.
CREATE UNIQUE INDEX bucket_claims_service_key
    ON bucket_claims (environment_id, service_key)
    WHERE owner_kind = 'service' AND phase <> 'released';
CREATE UNIQUE INDEX bucket_claims_system_key
    ON bucket_claims (system_key)
    WHERE owner_kind = 'system' AND phase <> 'released';
CREATE INDEX bucket_claims_environment_idx ON bucket_claims (environment_id);

-- The generated bucket identity on a store: S3 bucket name, access key id,
-- the NAME of the credential Secret in skali-platform, and the published
-- endpoint. Secret access keys live only in Kubernetes Secrets and their
-- environment mirrors, never in rows (REWORK_V2 5.8); credential_version
-- counts rotations so consumers can roll on change. With one live store per
-- installation the allocation's store reference is the placement; a
-- placements table arrives only if multi-store ever does.
CREATE TABLE bucket_allocations (
    id                 UUID PRIMARY KEY,
    claim_id           UUID NOT NULL REFERENCES bucket_claims(id),
    store_id           UUID NOT NULL REFERENCES object_stores(id),
    bucket_name        TEXT NOT NULL CHECK (bucket_name ~ '^[a-z][a-z0-9-]{1,61}[a-z0-9]$'),
    access_key_id      TEXT NOT NULL,
    credential_secret  TEXT NOT NULL,
    credential_version BIGINT NOT NULL DEFAULT 1,
    -- The endpoint published to consumers at allocation time: the in-cluster
    -- service URL, or the public URL when the installation has an external
    -- S3 domain. A record of what was published, not a rendering input.
    endpoint           TEXT NOT NULL,
    region             TEXT NOT NULL DEFAULT 'us-east-1',
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    released_at        TIMESTAMPTZ
);
-- One live allocation per claim, and unique live identities on the store.
CREATE UNIQUE INDEX bucket_allocations_claim_key
    ON bucket_allocations (claim_id) WHERE released_at IS NULL;
CREATE UNIQUE INDEX bucket_allocations_name_key
    ON bucket_allocations (bucket_name) WHERE released_at IS NULL;
CREATE UNIQUE INDEX bucket_allocations_access_key
    ON bucket_allocations (access_key_id) WHERE released_at IS NULL;

-- +goose Down
DROP TABLE bucket_allocations;
DROP TABLE bucket_claims;
DROP TABLE object_stores;
