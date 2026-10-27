-- +goose Up

-- One build's insights report: what the builder printed and how long each
-- of its steps took. The client that ran the build uploads it once the
-- build ended, so build output never streams into the journal line by
-- line. Keyed by the build step, so run retention removes it with its run.
-- The log is redacted at upload and stored gzip-compressed.
CREATE TABLE build_insights (
    step_id       UUID PRIMARY KEY REFERENCES steps(id) ON DELETE CASCADE,
    outcome       TEXT NOT NULL CHECK (outcome IN ('succeeded', 'failed')),
    engine        TEXT NOT NULL DEFAULT '',
    platform      TEXT NOT NULL DEFAULT '',
    started_at    TIMESTAMPTZ NOT NULL,
    finished_at   TIMESTAMPTZ NOT NULL,
    error         TEXT NOT NULL DEFAULT '',
    steps         JSONB NOT NULL DEFAULT '[]'::jsonb,
    log_gzip      BYTEA NOT NULL,
    log_lines     INT NOT NULL DEFAULT 0,
    log_truncated BOOLEAN NOT NULL DEFAULT false,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (finished_at >= started_at)
);

-- +goose Down
DROP TABLE build_insights;
