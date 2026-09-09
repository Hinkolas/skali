-- +goose Up

-- Fresh-install baseline for v0.1.0-alpha.1. Append future migrations;
-- never rewrite or renumber this baseline after the release.
-- This marker prevents the old development migration 1 from being mistaken
-- for this baseline by the embedded migration runner.
CREATE TABLE skali_schema_baseline (
    version TEXT PRIMARY KEY CHECK (version = 'v0.1.0-alpha.1')
);
INSERT INTO skali_schema_baseline (version) VALUES ('v0.1.0-alpha.1');

-- App-user accounts. Login is by email (case-insensitive). skali is
-- self-hosted: users are created by the operator via `skalid user create`;
-- there is no public signup endpoint.
CREATE TABLE users (
    id                 UUID PRIMARY KEY,
    email              TEXT NOT NULL,
    name               TEXT NOT NULL DEFAULT '',
    two_factor_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    role               TEXT NOT NULL DEFAULT 'member'
                       CHECK (role IN ('admin', 'member')),
    -- Members need this grant to create projects; instance admins always may.
    create_projects    BOOLEAN NOT NULL DEFAULT FALSE
);
-- Functional index instead of citext: avoids CREATE EXTENSION for self-hosters.
-- Every email lookup must go through lower(email) = lower($1).
CREATE UNIQUE INDEX users_email_key ON users (lower(email));

-- Login methods, separate from users (better-auth shape): today only
-- provider_id = 'credential' (password), leaving room for optional OIDC later.
CREATE TABLE accounts (
    id          UUID PRIMARY KEY,
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider_id TEXT NOT NULL,
    password    TEXT, -- argon2id PHC string; only for provider_id = 'credential'
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, provider_id)
);

-- Bearer sessions. token_hash is sha256 of the opaque token; the plaintext
-- token exists only in the login response.
CREATE TABLE sessions (
    id                 UUID PRIMARY KEY,
    user_id            UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash         BYTEA NOT NULL UNIQUE,
    expires_at         TIMESTAMPTZ NOT NULL,
    ip_address         TEXT NOT NULL DEFAULT '',
    user_agent         TEXT NOT NULL DEFAULT '',
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Last successful identity proof, used by sudo-mode freshness checks.
    reauthenticated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX sessions_user_id_idx ON sessions (user_id);
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);

-- TOTP enrollment, one per user. secret is AES-256-GCM encrypted
-- (version byte || nonce || ciphertext), keyed from AUTH_SECRET.
-- confirmed_at IS NULL means enrollment is pending the first valid code.
-- last_used_step only ever advances, which rejects TOTP replay atomically.
CREATE TABLE two_factors (
    id             UUID PRIMARY KEY,
    user_id        UUID NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    secret         BYTEA NOT NULL,
    confirmed_at   TIMESTAMPTZ,
    last_used_step BIGINT NOT NULL DEFAULT 0,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Single-use 2FA recovery codes. Hashed with argon2id, not sha256: the codes
-- are short enough to brute-force offline if the database leaks.
CREATE TABLE backup_codes (
    id            UUID PRIMARY KEY,
    two_factor_id UUID NOT NULL REFERENCES two_factors(id) ON DELETE CASCADE,
    code_hash     TEXT NOT NULL,
    used_at       TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX backup_codes_two_factor_id_idx ON backup_codes (two_factor_id);

-- Short-lived tokens bridging password login and 2FA verification for users
-- with 2FA enabled. DB-backed so they survive restarts and multiple daemon
-- instances. attempts caps code guesses per challenge.
CREATE TABLE login_challenges (
    id         UUID PRIMARY KEY,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash BYTEA NOT NULL UNIQUE,
    attempts   INT NOT NULL DEFAULT 0,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Product definition plane: projects, their environments, and versioned
-- project definitions. Nothing here mirrors cluster objects.
CREATE TABLE projects (
    id           UUID PRIMARY KEY,
    name         TEXT NOT NULL CHECK (name ~ '^[a-z][a-z0-9-]{0,62}$'),
    display_name TEXT NOT NULL DEFAULT '',
    -- Explicit draft source mode: 'managed' documents are edited through the
    -- web/API, 'file' documents are replaced by CLI/Git submissions. The mode
    -- gates which mutation path is accepted; switching is an explicit action.
    source_mode  TEXT NOT NULL DEFAULT 'managed'
                 CHECK (source_mode IN ('managed', 'file')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX projects_name_key ON projects (name);

-- Immutable, content-addressed compiled definitions. Created by draft
-- submissions and by deploy candidates; never updated while referenced.
-- Resubmitting identical content reuses the row via the content-hash unique.
CREATE TABLE definition_versions (
    id               UUID PRIMARY KEY,
    project_id       UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    schema_version   TEXT NOT NULL,
    definition_hash  TEXT NOT NULL,
    -- Canonical compiler.ProjectDefinition; the authoritative compiled form.
    definition       JSONB NOT NULL,
    -- Original manifest bytes for display and diffing only; never re-parsed
    -- as authority. The compiled definition above is what deploys.
    source           BYTEA NOT NULL,
    format           TEXT NOT NULL CHECK (format IN ('yaml', 'json')),
    compiler_version TEXT NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, definition_hash)
);
CREATE INDEX definition_versions_project_id_idx ON definition_versions (project_id);

-- One current draft per project. version is the optimistic concurrency token
-- (ETag): every accepted mutation bumps it, and submissions carrying a stale
-- expected version are rejected instead of silently overwriting.
CREATE TABLE project_drafts (
    project_id            UUID PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    version               BIGINT NOT NULL DEFAULT 1,
    definition_version_id UUID NOT NULL REFERENCES definition_versions(id),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- An environment is a remote copy of a project inside one installation, with
-- independent values, secrets, targets, and history.
CREATE TABLE environments (
    id            UUID PRIMARY KEY,
    project_id    UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name          TEXT NOT NULL CHECK (name ~ '^[a-z][a-z0-9-]{0,62}$'),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, name),
    CONSTRAINT environments_id_project_id_key UNIQUE (id, project_id),
    -- Server-side access ceiling, deployment protection, and scheduling priority.
    max_role      TEXT NOT NULL DEFAULT 'admin'
                  CHECK (max_role IN ('none', 'read', 'deploy', 'maintain', 'admin')),
    deploy_policy TEXT NOT NULL DEFAULT 'direct'
                  CHECK (deploy_policy IN ('direct', 'promote-only')),
    promote_from  TEXT[] NOT NULL DEFAULT '{}',
    priority      TEXT NOT NULL DEFAULT 'normal'
                  CHECK (priority IN ('normal', 'high'))
);
CREATE INDEX environments_project_id_idx ON environments (project_id);

-- Encrypted environment values, append-only per-name versions. Rows move
-- staged -> current -> superseded; old revisions retain their referenced values.
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
-- Deployment promotion writes target_revision_id; reconciliation owns
-- active_revision_id. A daemon restart never writes this table.
CREATE TABLE environment_targets (
    environment_id     UUID PRIMARY KEY REFERENCES environments(id) ON DELETE CASCADE,
    target_revision_id UUID REFERENCES revisions(id),
    active_revision_id UUID REFERENCES revisions(id),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    state              TEXT NOT NULL DEFAULT 'active'
                       CHECK (state IN ('active', 'down', 'releasing')),
    restarted_at       TIMESTAMPTZ
);

-- The run journal: runs -> steps -> attempts -> logs.
-- Runs explain reconciliation but never drive it: no authoritative table
-- references these rows, and deleting any or all of them must not change
-- targets, recovery, or controller behavior.
CREATE TABLE runs (
    id                UUID PRIMARY KEY,
    kind              TEXT NOT NULL,
    project_id        UUID REFERENCES projects(id) ON DELETE CASCADE,
    environment_id    UUID REFERENCES environments(id) ON DELETE CASCADE,
    actor             TEXT NOT NULL DEFAULT '',
    status            TEXT NOT NULL DEFAULT 'pending'
                      CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'cancelled')),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at        TIMESTAMPTZ,
    finished_at       TIMESTAMPTZ,
    bypass_protection BOOLEAN NOT NULL DEFAULT FALSE
);
CREATE INDEX runs_environment_id_idx ON runs (environment_id, created_at DESC);
-- One in-flight rollout per environment; queued work stays pending.
CREATE UNIQUE INDEX runs_environment_running_key
    ON runs (environment_id) WHERE status = 'running' AND environment_id IS NOT NULL;

-- key is the deterministic reattach anchor: a restarted controller finds
-- the same logical step by (run_id, key) instead of duplicating it.
CREATE TABLE steps (
    id               UUID PRIMARY KEY,
    run_id           UUID NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    parent_id        UUID REFERENCES steps(id) ON DELETE CASCADE,
    key              TEXT NOT NULL,
    title            TEXT NOT NULL,
    status           TEXT NOT NULL DEFAULT 'pending'
                     CHECK (status IN ('pending', 'waiting', 'running',
                                       'succeeded', 'failed', 'skipped', 'cancelled')),
    progress_current BIGINT,
    progress_total   BIGINT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at       TIMESTAMPTZ,
    finished_at      TIMESTAMPTZ,
    UNIQUE (run_id, key)
);
CREATE INDEX steps_run_id_idx ON steps (run_id);

-- An attempt exists only once it has started, so running is its initial
-- status. executor_id is the per-boot daemon identity; recovery fails
-- attempts whose executor no longer exists.
CREATE TABLE attempts (
    id          UUID PRIMARY KEY,
    step_id     UUID NOT NULL REFERENCES steps(id) ON DELETE CASCADE,
    number      BIGINT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'running'
                CHECK (status IN ('running', 'succeeded', 'failed', 'cancelled')),
    executor_id TEXT NOT NULL,
    started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    UNIQUE (step_id, number)
);
-- A new attempt may start only after the previous one is terminal.
CREATE UNIQUE INDEX attempts_step_running_key
    ON attempts (step_id) WHERE status = 'running';

-- Append-only, bounded, redacted. seq is per-attempt and monotonic; it is
-- the SSE resume cursor. The caps (entries per attempt, bytes per message)
-- are enforced by the journal writer, not the schema.
CREATE TABLE run_logs (
    id         UUID PRIMARY KEY,
    attempt_id UUID NOT NULL REFERENCES attempts(id) ON DELETE CASCADE,
    seq        BIGINT NOT NULL,
    ts         TIMESTAMPTZ NOT NULL DEFAULT now(),
    level      TEXT NOT NULL CHECK (level IN ('debug', 'info', 'warn', 'error')),
    message    TEXT NOT NULL,
    fields     JSONB NOT NULL DEFAULT '{}'::jsonb,
    UNIQUE (attempt_id, seq)
);

-- Deployment coordination rows: the authoritative state of one
-- multi-request deployment (submit definition, stage values, build and
-- verify artifacts, complete). The journal run explains this work but never
-- drives it: run_id may be nulled by journal retention without changing
-- deployment behavior. Status machine in internal/deploy.
CREATE TABLE deployments (
    id                    UUID PRIMARY KEY,
    project_id            UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    environment_id        UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    definition_version_id UUID NOT NULL REFERENCES definition_versions(id),
    candidate_id          UUID,
    status                TEXT NOT NULL DEFAULT 'preparing'
                          CHECK (status IN ('preparing', 'promoted', 'failed', 'cancelled')),
    revision_id           UUID REFERENCES revisions(id),
    run_id                UUID REFERENCES runs(id) ON DELETE SET NULL,
    actor                 TEXT NOT NULL DEFAULT '',
    build_executor        TEXT NOT NULL DEFAULT 'local',
    -- The per-application artifact decisions taken at open (reuse, build,
    -- or import, with their artifact and build record ids); completion
    -- re-reads them so the server never trusts client-supplied artifact
    -- ids.
    actions               JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Deployment decisions persisted until transactional promotion.
    restart               BOOLEAN NOT NULL DEFAULT FALSE,
    local_applications    JSONB,
    prune_values          BOOLEAN NOT NULL DEFAULT FALSE,
    bypass_protection     BOOLEAN NOT NULL DEFAULT FALSE,
    from_environment_id   UUID REFERENCES environments(id) ON DELETE SET NULL
);
-- One deployment prepares per environment at a time.
CREATE UNIQUE INDEX deployments_environment_preparing_key
    ON deployments (environment_id) WHERE status = 'preparing';
CREATE INDEX deployments_environment_id_idx
    ON deployments (environment_id, created_at DESC);

-- Build records: every build regardless of where it ran. Local builds are
-- records, not queue entries: the CLI creates them already running and they
-- can never be claimed. The queued/claimed states and the lease columns are
-- the worker protocol reserved for future remote builds.
-- Logs are never stored here; run_id and step_key reference the journal,
-- and journal deletion never affects build state.
CREATE TABLE builds (
    id               UUID PRIMARY KEY,
    project_id       UUID REFERENCES projects(id) ON DELETE SET NULL,
    deployment_id    UUID REFERENCES deployments(id) ON DELETE SET NULL,
    application      TEXT NOT NULL DEFAULT '',
    origin           TEXT NOT NULL CHECK (origin IN ('local', 'worker')),
    status           TEXT NOT NULL CHECK (status IN
                     ('queued', 'claimed', 'running', 'succeeded', 'failed', 'cancelled')),
    platform         TEXT NOT NULL DEFAULT '',
    context_hash     TEXT NOT NULL DEFAULT '',
    config_hash      TEXT NOT NULL DEFAULT '',
    artifact_id      UUID REFERENCES artifacts(id),
    run_id           UUID REFERENCES runs(id) ON DELETE SET NULL,
    step_key         TEXT NOT NULL DEFAULT '',
    claimed_by       TEXT NOT NULL DEFAULT '',
    claimed_at       TIMESTAMPTZ,
    lease_expires_at TIMESTAMPTZ,
    heartbeat_at     TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at       TIMESTAMPTZ,
    finished_at      TIMESTAMPTZ,
    -- Local builds are never queue rows.
    CHECK (origin <> 'local' OR status IN ('running', 'succeeded', 'failed', 'cancelled'))
);
CREATE INDEX builds_deployment_id_idx ON builds (deployment_id);
CREATE INDEX builds_project_id_idx ON builds (project_id, created_at DESC);

-- The shared database substrate: claims,
-- physical clusters, placements, and tenants. These tables are
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
                   CHECK (state IN ('active', 'releasing', 'released')),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    node_port      INTEGER
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
-- Loopback NodePort of the pool's primary Service on the local platform.
-- Always NULL on managed clusters. The partial unique index frees the port
-- as soon as a pool is released.
CREATE UNIQUE INDEX database_clusters_node_port
    ON database_clusters (node_port)
    WHERE node_port IS NOT NULL AND state <> 'released';

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

-- The object-storage substrate: the physical
-- SeaweedFS system, logical bucket claims, and their allocations. These
-- tables are authoritative desired/durable state below product
-- services; live SeaweedFS topology (masters, volumes, usage) is never
-- mirrored here, it belongs to the observed store. The SeaweedFS metadata
-- database is an ordinary system claim in database_claims
-- ('system/object-storage/metadata'), never a table here.

-- The physical SeaweedFS system, rendered and applied by skalid in
-- skali-platform under the skalid-platform field manager. Exactly one live
-- store per installation; the shape columns record desired topology derived
-- from the object-storage node count at creation (the observed store reports
-- what actually runs).
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
                         CHECK (state IN ('active', 'releasing', 'released')),
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
    id                              UUID PRIMARY KEY,
    owner_kind                      TEXT NOT NULL CHECK (owner_kind IN ('service', 'system')),
    project_id                      UUID,
    environment_id                  UUID,
    service_key                     TEXT NOT NULL DEFAULT '',
    system_key                      TEXT NOT NULL DEFAULT '',
    owner_ref                       TEXT NOT NULL,
    visibility                      TEXT NOT NULL CHECK (visibility IN ('private', 'public-read')),
    storage_quota_bytes             BIGINT NOT NULL DEFAULT 0,
    object_quota                    BIGINT NOT NULL DEFAULT 0,
    max_object_bytes                BIGINT NOT NULL DEFAULT 0,
    versioning                      TEXT NOT NULL CHECK (versioning IN ('enabled', 'disabled')),
    abort_uploads_after_seconds     BIGINT NOT NULL DEFAULT 0,
    expire_noncurrent_after_seconds BIGINT NOT NULL DEFAULT 0,
    phase                           TEXT NOT NULL DEFAULT 'pending'
                                    CHECK (phase IN ('pending', 'bound', 'provisioned', 'releasing', 'released')),
    created_at                      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                      TIMESTAMPTZ NOT NULL DEFAULT now()
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
-- environment mirrors, never in rows; credential_version
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

-- The installation identity: minted once when this database is created and
-- never updated afterwards. Every API response carries it in the
-- Skali-Instance header so clients can pin it per remote and detect that a
-- cluster was uninstalled and reinstalled (which recreates this database and
-- therefore mints a new identity). It survives exactly what it should:
-- daemon restarts, upgrades, and backup restores. The singleton column keeps
-- the table at one row.
CREATE TABLE instance_identity (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    id        UUID NOT NULL
);
INSERT INTO instance_identity (id) VALUES (gen_random_uuid());

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

-- Applications a dev session runs on the host instead of in the cluster.
-- Each deploy replaces the environment's whole set: a deploy without local
-- applications clears it, which is exactly what skali dev --preview and
-- plain skali deploy do. ports maps the application's service port names to
-- the host ports the local dev process listens on.
CREATE TABLE environment_intercepts (
    environment_id  UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    application_key TEXT NOT NULL,
    ports           JSONB NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (environment_id, application_key)
);

-- Project membership: one role per (project, user) on the shared ladder
-- read < deploy < maintain < admin. The role is the member's default on
-- every environment of the project and carries the project-level rights
-- (docs/permissions.md). Non-members do not see the project at all. What a
-- role means lives in internal/authz, never here.
CREATE TABLE project_members (
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       TEXT NOT NULL CHECK (role IN ('read', 'deploy', 'maintain', 'admin')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, user_id)
);
CREATE INDEX project_members_user_id_idx ON project_members (user_id);

-- Cells override the project role for one user on one environment, up or
-- down, 'none' included (locked: listed by name, contents refused). A cell
-- hangs off the membership: the composite key to project_members drops the
-- cells when the member leaves, and the composite key to environments keeps
-- project_id honest (a cell can only reference an environment of the
-- project whose membership it extends).

CREATE TABLE environment_access (
    environment_id UUID NOT NULL,
    project_id     UUID NOT NULL,
    user_id        UUID NOT NULL,
    role           TEXT NOT NULL CHECK (role IN ('none', 'read', 'deploy', 'maintain', 'admin')),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (environment_id, user_id),
    FOREIGN KEY (environment_id, project_id)
        REFERENCES environments(id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (project_id, user_id)
        REFERENCES project_members(project_id, user_id) ON DELETE CASCADE
);
CREATE INDEX environment_access_project_user_idx ON environment_access (project_id, user_id);

-- Per-application restart stamps: a service restart stamps one application,
-- while deploy --force keeps stamping the whole environment on
-- environment_targets.restarted_at. The reconciler renders the later of the
-- two stamps as the pod-template restart annotation, so both compose and a
-- service restart rolls exactly one workload. Stateful services never read
-- either stamp: restarts recreate pods, never data.
CREATE TABLE environment_restarts (
    environment_id  UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    application_key TEXT NOT NULL,
    restarted_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (environment_id, application_key)
);

-- Usage telemetry sampled from metrics.k8s.io by the skalid metrics sampler.
-- Rows are per-application aggregates (summed over the app's pods at sample
-- time), not per-pod: the console charts applications, and per-pod rows would
-- multiply the table for no consumer. Raw 30s samples only, pruned by age;
-- read queries bucket with date_bin, so a rollup table can slot in behind the
-- same reads later without an API change.
CREATE TABLE metric_app_samples (
    environment_id  UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    application_key TEXT NOT NULL,
    sampled_at      TIMESTAMPTZ NOT NULL,
    cpu_millicores  BIGINT NOT NULL,
    memory_bytes    BIGINT NOT NULL,
    pod_count       BIGINT NOT NULL,
    PRIMARY KEY (environment_id, application_key, sampled_at)
);
CREATE INDEX metric_app_samples_sampled_at_idx ON metric_app_samples (sampled_at);

-- Allocatable is denormalized per sample so a node resize shows up in history
-- instead of rewriting it; the API serves the latest row's value as the
-- capacity scalar.
CREATE TABLE metric_node_samples (
    node_name                  TEXT NOT NULL,
    sampled_at                 TIMESTAMPTZ NOT NULL,
    cpu_millicores             BIGINT NOT NULL,
    memory_bytes               BIGINT NOT NULL,
    cpu_allocatable_millicores BIGINT NOT NULL,
    memory_allocatable_bytes   BIGINT NOT NULL,
    PRIMARY KEY (node_name, sampled_at)
);
CREATE INDEX metric_node_samples_sampled_at_idx ON metric_node_samples (sampled_at);

-- Edge traffic sampled from Traefik's per-router Prometheus counters. Rows
-- carry per-interval deltas (the sampler differences the raw counters in
-- memory, so restarts and counter resets never produce negative spikes) and
-- keep the route key for a future per-route breakdown; the API sums per
-- application. Same retention discipline as the usage tables.
CREATE TABLE metric_edge_samples (
    environment_id  UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    application_key TEXT NOT NULL,
    route_key       TEXT NOT NULL,
    sampled_at      TIMESTAMPTZ NOT NULL,
    requests        BIGINT NOT NULL,
    request_bytes   BIGINT NOT NULL,
    response_bytes  BIGINT NOT NULL,
    PRIMARY KEY (environment_id, application_key, route_key, sampled_at)
);
CREATE INDEX metric_edge_samples_sampled_at_idx ON metric_edge_samples (sampled_at);

-- Storage telemetry sampled every few minutes by the skalid metrics sampler
-- (storage moves slowly, so the cadence is decoupled from the 30s usage
-- tick). Node rows carry the filesystem totals plus the attributable
-- category rollups the console's capacity bar segments on; the remainder up
-- to used_bytes is the system share, computed at read time. Same retention
-- discipline as the usage tables.
CREATE TABLE metric_storage_node_samples (
    node_name       TEXT NOT NULL,
    sampled_at      TIMESTAMPTZ NOT NULL,
    capacity_bytes  BIGINT NOT NULL,
    used_bytes      BIGINT NOT NULL,
    available_bytes BIGINT NOT NULL,
    volumes_bytes   BIGINT NOT NULL,
    databases_bytes BIGINT NOT NULL,
    objects_bytes   BIGINT NOT NULL,
    images_bytes    BIGINT NOT NULL,
    PRIMARY KEY (node_name, sampled_at),
    temporary_bytes BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX metric_storage_node_samples_sampled_at_idx ON metric_storage_node_samples (sampled_at);

-- Per-service storage footprint: application volumes, databases, and
-- buckets, keyed by the environment and the product service key
-- (applications bare, databases and buckets prefixed like their status
-- identities). used_bytes is NULL where usage is unmeasurable (local-path
-- app volumes report whole-node statfs, which the sampler discards);
-- capacity_bytes is the declared size or quota, 0 when unknown.
CREATE TABLE metric_storage_samples (
    environment_id UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    service_key    TEXT NOT NULL,
    kind           TEXT NOT NULL,
    sampled_at     TIMESTAMPTZ NOT NULL,
    used_bytes     BIGINT,
    capacity_bytes BIGINT NOT NULL,
    PRIMARY KEY (environment_id, service_key, kind, sampled_at)
);
CREATE INDEX metric_storage_samples_sampled_at_idx ON metric_storage_samples (sampled_at);

-- Browser device authorization for the CLI. A device request is the
-- short-lived handshake between a terminal and a browser: the CLI creates
-- one, shows the user code, and polls with the device code; the signed-in
-- browser looks the user code up and approves or denies it. Two intents:
-- login mints a session for the approver on the CLI's next poll; reauth
-- re-stamps the bound CLI session (session_id) with the approver's fresh
-- identity, so a sudo-gated command confirms in the browser instead of
-- retyping the password. Only the device code's hash is stored; the
-- plaintext lives in the CLI process alone.
CREATE TABLE device_requests (
    id               UUID PRIMARY KEY,
    intent           TEXT NOT NULL CHECK (intent IN ('login', 'reauth')),
    user_code        TEXT NOT NULL UNIQUE,
    device_code_hash BYTEA NOT NULL UNIQUE,
    status           TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'denied')),
    -- reauth: the CLI session the approval re-stamps; login: unused.
    session_id       UUID REFERENCES sessions(id) ON DELETE CASCADE,
    -- reauth: the session's owner, set at creation; login: the approver,
    -- set at approval.
    user_id          UUID REFERENCES users(id) ON DELETE CASCADE,
    client_label     TEXT NOT NULL DEFAULT '',
    ip_address       TEXT NOT NULL DEFAULT '',
    user_agent       TEXT NOT NULL DEFAULT '',
    poll_count       INTEGER NOT NULL DEFAULT 0,
    last_polled_at   TIMESTAMPTZ,
    expires_at       TIMESTAMPTZ NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX device_requests_expires_at_idx ON device_requests (expires_at);

-- Console-driven platform updates: the operator's channel and auto-update
-- choice plus the result of the last release scan, one row per
-- installation (the singleton column keeps it at one, seeded here so reads
-- never miss). What is installed is not recorded: the running daemon knows
-- its own version and the cluster state knows every node's, so this row
-- only remembers what was found upstream and when, which is what a page
-- load must answer without a network round trip.
CREATE TABLE update_settings (
    singleton           BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    channel             TEXT NOT NULL DEFAULT 'stable' CHECK (channel IN ('stable', 'beta')),
    auto_update         BOOLEAN NOT NULL DEFAULT FALSE,
    last_checked_at     TIMESTAMPTZ,
    latest_version      TEXT,
    latest_k3s          TEXT,
    latest_published_at TIMESTAMPTZ,
    latest_url          TEXT,
    last_error          TEXT,
    last_error_kind     TEXT CHECK (last_error_kind IN ('offline', 'not_found', 'rate_limited', 'unavailable', 'invalid')),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO update_settings DEFAULT VALUES;

CREATE TABLE hostname_claims (
    hostname           TEXT PRIMARY KEY,
    environment_id     UUID REFERENCES environments(id) ON DELETE RESTRICT,
    target_revision_id UUID REFERENCES revisions(id),
    reserved           BOOLEAN NOT NULL DEFAULT false,
    CHECK ((reserved AND environment_id IS NULL AND target_revision_id IS NULL) OR (NOT reserved AND environment_id IS NOT NULL))
);
CREATE INDEX hostname_claims_environment ON hostname_claims(environment_id);

-- +goose Down

DROP TABLE hostname_claims;
DROP TABLE update_settings;
DROP TABLE device_requests;
DROP TABLE metric_storage_samples;
DROP TABLE metric_storage_node_samples;
DROP TABLE metric_edge_samples;
DROP TABLE metric_node_samples;
DROP TABLE metric_app_samples;
DROP TABLE environment_restarts;
DROP TABLE environment_access;
DROP TABLE project_members;
DROP TABLE environment_intercepts;
DROP TABLE backups;
DROP TABLE backup_targets;
DROP TABLE instance_identity;
DROP TABLE bucket_allocations;
DROP TABLE bucket_claims;
DROP TABLE object_stores;
DROP TABLE database_tenants;
DROP TABLE database_placements;
DROP TABLE database_claims;
DROP TABLE database_clusters;
DROP TABLE builds;
DROP TABLE deployments;
DROP TABLE run_logs;
DROP TABLE attempts;
DROP TABLE steps;
DROP TABLE runs;
DROP TABLE environment_targets;
DROP TABLE artifact_leases;
DROP TABLE revisions;
DROP TABLE artifacts;
DROP TABLE environment_secrets;
DROP TABLE environments;
DROP TABLE project_drafts;
DROP TABLE definition_versions;
DROP TABLE projects;
DROP TABLE login_challenges;
DROP TABLE backup_codes;
DROP TABLE two_factors;
DROP TABLE sessions;
DROP TABLE accounts;
DROP TABLE users;
DROP TABLE skali_schema_baseline;
