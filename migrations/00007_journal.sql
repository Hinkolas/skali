-- +goose Up

-- The run journal: runs -> steps -> attempts -> logs.
-- Runs explain reconciliation but never drive it: no authoritative table
-- references these rows, and deleting any or all of them must not change
-- targets, recovery, or controller behavior.
CREATE TABLE runs (
    id             UUID PRIMARY KEY,
    kind           TEXT NOT NULL,
    project_id     UUID REFERENCES projects(id) ON DELETE CASCADE,
    environment_id UUID REFERENCES environments(id) ON DELETE CASCADE,
    actor          TEXT NOT NULL DEFAULT '',
    status         TEXT NOT NULL DEFAULT 'pending'
                   CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'cancelled')),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at     TIMESTAMPTZ,
    finished_at    TIMESTAMPTZ
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

-- +goose Down
DROP TABLE run_logs;
DROP TABLE attempts;
DROP TABLE steps;
DROP TABLE runs;
