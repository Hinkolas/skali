-- +goose Up

-- Task-shaped background work kicked off by a user request (registry imports
-- today): inserted running, finished exactly once, read by polling clients.
-- State-shaped convergence (workloads) is tracked on its own rows, not here.
-- expires_at is the liveness story: work runs in-process on the master, so a
-- row still running past its deadline belongs to a dead or wedged process
-- and is failed by the hygiene sweep — no owner bookkeeping needed.
CREATE TABLE operations (
    id          UUID PRIMARY KEY,
    kind        TEXT NOT NULL,                  -- e.g. registry_import
    status      TEXT NOT NULL DEFAULT 'running'
                CHECK (status IN ('running', 'succeeded', 'failed')),
    subject     TEXT NOT NULL DEFAULT '',       -- kind-specific, e.g. the upstream reference
    result      JSONB NOT NULL DEFAULT '{}',    -- kind-specific success payload
    error       TEXT,
    created_by  UUID REFERENCES users(id) ON DELETE SET NULL,
    expires_at  TIMESTAMPTZ NOT NULL,           -- created_at + the operation's timeout (+ slack)
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ
);

CREATE INDEX operations_created_at_idx ON operations (created_at DESC);

-- +goose Down
DROP TABLE operations;
