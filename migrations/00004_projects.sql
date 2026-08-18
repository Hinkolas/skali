-- +goose Up

-- Product definition plane: projects, their
-- environments, and versioned project definitions. Runtime state lives in
-- later migrations; nothing here mirrors cluster objects.
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
    id         UUID PRIMARY KEY,
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name       TEXT NOT NULL CHECK (name ~ '^[a-z][a-z0-9-]{0,62}$'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, name)
);
CREATE INDEX environments_project_id_idx ON environments (project_id);

-- +goose Down
DROP TABLE environments;
DROP TABLE project_drafts;
DROP TABLE definition_versions;
DROP TABLE projects;
