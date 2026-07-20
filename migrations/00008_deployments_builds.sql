-- +goose Up

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
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- One deployment prepares per environment at a time.
CREATE UNIQUE INDEX deployments_environment_preparing_key
    ON deployments (environment_id) WHERE status = 'preparing';
CREATE INDEX deployments_environment_id_idx
    ON deployments (environment_id, created_at DESC);

-- Build records: every build regardless of where it ran. Local builds are
-- records, not queue entries: the CLI creates them already running and they
-- can never be claimed. The queued/claimed states and the lease columns are
-- the R4 worker protocol, designed now so workers add no schema rework.
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

-- +goose Down
DROP TABLE builds;
DROP TABLE deployments;
